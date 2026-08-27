# asqs-core on OpenShift — Deployment Guide

Deploying the open-source **asqs-core** CLI (`github.com/asqs/asqs-core`) inside an OpenShift
cluster. Companion to `DEPLOYMENT-GUIDE.md` (which covers the commercial `asqs-go` +
`asqs-react` stack on VMs/bare metal); this document is standalone and OpenShift-specific.

---

## 1. What asqs-core is — and what that means on OpenShift

asqs-core is **a single CLI binary with one command**: `asqs-core run`. Verified in
`cmd/asqs-core/main.go` — there is no HTTP server, no control-plane API, no webhook
listener, and no web UI in this repo (those are the enterprise layer in `asqs-go`).

The practical consequence for OpenShift: **asqs-core is not a Deployment.** It is a
batch workload.

| Shape | Use for |
| --- | --- |
| **Job** | One-shot run against a repo (manual trigger, or created by your CI). |
| **CronJob** | Recurring runs (the "nightly quality pass" pattern). The open core has no scheduler of its own; OpenShift's cron is the mechanism. |
| ~~Deployment~~ | Not applicable — the process exits when the run finishes. |

Everything else in the cluster (Postgres, image, config) exists to serve those Jobs.

**Pipeline recap:** `bootstrap → index → plan → generate → evaluate (compile + test + LLM
fixer loop) → optional ship (push branch + open PR)`. The **evaluate** phase is the one that
wants to run containers — which is the subject of the next section.

---

## 2. Can you run Docker containers inside the pod, like the evaluation phase does?

**Short answer: not the way it works on a normal Docker host. The socket-mounting pattern is
impossible on OpenShift, and true Docker-in-Docker is possible but almost always blocked by
cluster policy. The recommended OpenShift configuration is `general.sandbox.type: local`.**

Here is the reasoning in full, because this decision shapes the whole deployment.

### 2.1 Why "mount the Docker socket" (Docker-outside-of-Docker) cannot work

This is the pattern the VM guide uses (`-v /var/run/docker.sock:/var/run/docker.sock`). On
OpenShift it fails for two independent reasons:

1. **There is no Docker daemon on the node.** OpenShift 4 uses **CRI-O** as its container
   runtime. `/var/run/docker.sock` does not exist to be mounted. (And mounting any hostPath
   requires the `hostmount-anyuid` or `privileged` SCC, which the default `restricted-v2`
   forbids.)

2. **Even with a daemon, the bind mounts would resolve in the wrong filesystem.** asqs-core
   builds its eval invocation in `internal/runner/jobrunner/docker.go`:

   ```
   docker run --rm --init -v <HostWorkDir>:/workspace:rw -w /workspace \
     --network <net> [--memory …] [--cpus …] [--pids-limit …] --cidfile … <image> <cmd>
   ```

   `HostWorkDir` is the repo checkout path **inside the asqs-core pod**. A daemon running
   outside the pod resolves that path in *its own* filesystem — where it does not exist — so
   the eval container would start against an empty or missing directory. This is a general
   property of socket-mounted Docker, not an asqs-core bug.

### 2.2 True Docker-in-Docker (privileged sidecar) — possible, usually forbidden

Running a real `docker:dind` daemon as a sidecar **does** work functionally, and it fixes
the path problem (see 8.1), but it requires `privileged: true`, which means binding your
service account to the **`privileged` SCC**. On most enterprise and all regulated OpenShift
clusters this requires cluster-admin sign-off and is commonly refused outright, because a
privileged container can escape to the node.

If your platform team will grant it, section 8 has the full manifest. Treat it as the
fallback, not the default.

### 2.3 Rootless Podman inside the pod — the middle ground

asqs-core makes the container CLI configurable: `general.sandbox.docker.binary` (env
`RUNNER_DOCKER_BINARY`, `internal/config/config.go:696`). Podman is argv-compatible with
every flag asqs-core emits (`run --rm --init -v -w --network --memory --cpus --pids-limit
--cidfile --read-only --tmpfs --ipc=host`), so `docker_binary: podman` is a drop-in change.

Rootless Podman in a pod needs less than full privilege — typically `/dev/fuse` for
fuse-overlayfs, `SETUID`/`SETGID` capabilities, and a user-namespace-capable SCC — but it is
still **elevated beyond `restricted-v2`** and still needs a custom SCC from your platform
team. It has one real advantage over the DinD sidecar: Podman runs in the *same* container
as asqs-core, so bind-mount paths are consistent with no extra plumbing. Section 8.2.

### 2.4 The recommendation: `general.sandbox.type: local`

Set `general.sandbox.type: local` and **bake the language toolchains into the asqs-core image**. The
eval then runs Maven/Gradle/npm/dotnet as child processes inside the pod — no nesting, no
custom SCC, fully `restricted-v2` compliant.

The mental shift: **on Kubernetes, the pod *is* the sandbox.** The isolation that
`docker run --rm` provides on a VM is provided here by running each pipeline execution as a
fresh, short-lived, unprivileged pod.

**What you give up, stated honestly:**

- **One image must carry every toolchain you target.** You lose per-language image switching
  (`image_java_maven`, `image_node`, …) and the JDK-11-vs-21 profile flexibility. Pin the
  toolchain versions your target repos actually need; if you must support both JDK 11 and 21,
  either install both and select via `JAVA_HOME`/`general.build.build_tool`, or run separate Jobs
  from separate images.
- **You lose the per-step network isolation.** In Docker mode, compile/test default to
  `--network none` after dependency restore. In local mode there is no such boundary — use a
  **NetworkPolicy** on the Job pod to get the equivalent (section 9.3).
- **Generated tests execute in the same container as asqs-core.** These are LLM-written
  programs, and they run with the pod's environment — including the LLM API key and any VCS
  token in env. Mitigations, all of which you should apply: one Job per run so the pod is
  ephemeral; `automountServiceAccountToken: false`; a dedicated service account with no RBAC;
  a NetworkPolicy restricting egress; and keeping the ship/VCS token out of the run pod when
  you don't use `--ship` (do the PR step separately).
- **`general.sandbox.type: kubernetes` is not available.** It is declared as "(future)" in
  `internal/config/config.go:664` and is **not implemented** — `internal/runner/runner.go`
  dispatches only `local` and `docker`. A per-eval-pod backend would be the ideal OpenShift
  design; it does not exist today.

**Same decision applies to the indexers.** `indexer.execution` accepts `local` or `docker`
(`config.example.yaml:24`). Use **`local`** and rely on the JDK/Node in your image. Likewise
set the bootstrap `execution: local` under `bootstrap.test_framework` /
`e2e_framework_bootstrap`.

### 2.5 Summary table

| Strategy | Config | SCC needed | Verdict |
| --- | --- | --- | --- |
| Socket mount (DooD) | — | impossible | **Cannot work** — no Docker on CRI-O nodes; bind paths resolve in the wrong filesystem. |
| **Local toolchains** | `general.sandbox.type: local` | `restricted-v2` (default) | **Recommended.** Start here. |
| Rootless Podman in-pod | `general.sandbox.type: docker`, `docker_binary: podman` | custom (userns, `/dev/fuse`) | Viable if you need per-step isolation and can get an SCC. |
| DinD sidecar | `general.sandbox.type: docker`, `DOCKER_HOST=tcp://localhost:2375` | `privileged` | Works; usually refused. Last resort. |

---

## 3. Container image

### 3.1 OpenShift image rules that matter

OpenShift runs your container as a **random UID** from the namespace's range, with **GID 0**.
Two consequences that break naively-built images:

- Every directory the process writes to must be **group-0 writable** (`chgrp -R 0 <dir> &&
  chmod -R g=u <dir>`).
- `HOME` may resolve to `/`, which is not writable. Toolchains then fail trying to create
  `~/.m2`, `~/.npm`, `~/.gradle`. Set `HOME` explicitly to a prepared, group-writable
  directory.
- The `USER` instruction must be **numeric** (`USER 1001`, not `USER asqs`) or the
  `runAsNonRoot` check cannot verify it and the pod fails to start.

asqs-core also creates temp directories via `os.MkdirTemp` (`cmd/asqs-core/main.go:110`, used
for clone-URL checkouts), so `/tmp` must be writable — mount an `emptyDir` there if you set a
read-only root filesystem.

### 3.2 Dockerfile

`asqs-core/Dockerfile.openshift`:

```dockerfile
# ---------- build ----------
FROM registry.access.redhat.com/ubi9/go-toolset:1.24 AS build
WORKDIR /opt/app-root/src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /tmp/asqs-core ./cmd/asqs-core

# ---------- indexers ----------
# Built here so the runtime image needs no build tooling for them.
FROM registry.access.redhat.com/ubi9/openjdk-21 AS java-indexer
WORKDIR /src
COPY tools/java-indexer /src
RUN mvn -q -B package

FROM registry.access.redhat.com/ubi9/nodejs-20 AS js-indexer
WORKDIR /src
COPY tools/js-ts-indexer /src
RUN npm ci && npm run build

# ---------- runtime ----------
FROM registry.access.redhat.com/ubi9/ubi:latest

# Toolchains for runner.type: local. Trim to the languages you actually target.
RUN dnf -y install --setopt=install_weak_deps=False \
      java-21-openjdk-devel maven git tar gzip \
 && dnf -y module enable nodejs:20 && dnf -y install nodejs npm \
 && dnf clean all && rm -rf /var/cache/dnf
# Optional: .NET targets
# RUN dnf -y install dotnet-sdk-9.0 && dnf clean all
# Optional: Gradle (no RPM — install the distribution)
# RUN curl -fsSL -o /tmp/g.zip https://services.gradle.org/distributions/gradle-8.11-bin.zip \
#  && unzip -q /tmp/g.zip -d /opt && ln -s /opt/gradle-8.11/bin/gradle /usr/local/bin/gradle && rm /tmp/g.zip

ENV HOME=/home/asqs \
    APP_ROOT=/opt/asqs \
    MAVEN_CONFIG=/home/asqs/.m2 \
    npm_config_cache=/home/asqs/.npm \
    GRADLE_USER_HOME=/home/asqs/.gradle \
    NUGET_PACKAGES=/home/asqs/.nuget/packages \
    XDG_CACHE_HOME=/home/asqs/.cache

COPY --from=build        /tmp/asqs-core                                  /usr/local/bin/asqs-core
COPY --from=java-indexer /src/target/java-indexer-0.1.0.jar              ${APP_ROOT}/tools/java-indexer/target/
COPY --from=js-indexer   /src/dist                                       ${APP_ROOT}/tools/js-ts-indexer/dist/

# Arbitrary-UID support: group 0 owns and can write everything the process touches.
RUN mkdir -p /home/asqs/.m2 /home/asqs/.npm /home/asqs/.gradle /home/asqs/.cache \
             ${APP_ROOT} /workspace \
 && chgrp -R 0 /home/asqs ${APP_ROOT} /workspace \
 && chmod -R g=u /home/asqs ${APP_ROOT} /workspace

WORKDIR ${APP_ROOT}
USER 1001
ENTRYPOINT ["asqs-core"]
CMD ["run", "--help"]
```

> **Note on the `.NET` and Gradle lines:** they are commented out deliberately. Every
> toolchain you add inflates the image (the full Java+Node+.NET set lands around 1.5–2 GB).
> Install only what your target repos use.

### 3.3 Building it in-cluster

Either build externally and push to your registry, or use an OpenShift **BuildConfig** so the
image is produced by the cluster (works well behind a proxy, and lands straight in the
internal registry):

```bash
oc new-project asqs

# Build from a git repo (add --context-dir if asqs-core is a subdirectory)
oc new-build --name=asqs-core \
  --strategy=docker \
  --dockerfile-path=Dockerfile.openshift \
  https://bitbucket.example.com/scm/asqs/asqs-core.git

oc start-build asqs-core --follow
```

Or build locally and push:

```bash
podman build -f Dockerfile.openshift -t asqs-core:latest .
podman tag asqs-core:latest default-route-openshift-image-registry.apps.<cluster>/asqs/asqs-core:latest
podman push default-route-openshift-image-registry.apps.<cluster>/asqs/asqs-core:latest
```

The image is then referenced as `image-registry.openshift-image-registry.svc:5000/asqs/asqs-core:latest`
from inside the cluster.

**Troubleshooting the build:** if `dnf` fails to reach repos, your cluster proxy is not
reaching the build pod — set the proxy on the BuildConfig (`oc set env bc/asqs-core
HTTP_PROXY=… HTTPS_PROXY=… NO_PROXY=…`) and confirm the cluster-wide `Proxy` object is
configured. For a disconnected cluster, point the base images at your mirror registry.

---

## 4. Database: PostgreSQL + pgvector

asqs-core needs **Postgres with pgvector** (HNSW index). The schema is created automatically
on first run — `InitSchema` in both `internal/storage/metadata/store.go:42` and
`internal/storage/embeddings/store.go:81` executes an embedded `schema.sql` with
`CREATE TABLE IF NOT EXISTS`, and runs `CREATE EXTENSION IF NOT EXISTS vector` itself. **There
is no migration step to run.**

One useful detail: the embeddings schema ships `vector(1536)` but `InitSchema` **rewrites the
dimension** from `general.llm.embeddings.dimension` before executing
(`internal/storage/embeddings/store.go:92-95`). So a 768-dimension model (e.g. Ollama
`nomic-embed-text`) works on a fresh database — just set the config value correctly *before*
the first run, because changing it later requires re-indexing.

### 4.1 Choosing how to run Postgres

| Option | When to use | Notes |
| --- | --- | --- |
| **External / managed DB** | The client already runs Postgres. | Simplest. Needs pgvector installed **server-side** by their DBA, and the asqs role must be able to `CREATE EXTENSION` (DB owner or superuser). |
| **Postgres operator** (CloudNativePG, Crunchy Postgres for Kubernetes — both in OperatorHub) | Production in-cluster. | **Recommended in-cluster path.** Operators handle SCC, storage, backups, and failover properly. Confirm the operator's image includes pgvector, or use its documented extension-image mechanism. |
| **Plain Deployment with the community image** | Dev/PoC only. | The community `pgvector/pgvector:pg16` image expects a fixed UID and **will not start under `restricted-v2`**. It needs the `anyuid` SCC — i.e. the same kind of policy exception this guide is trying to avoid. |

### 4.2 Dev/PoC StatefulSet (community image, requires `anyuid`)

Only if your platform team grants `anyuid` for the DB service account:

```bash
oc create sa asqs-postgres
oc adm policy add-scc-to-user anyuid -z asqs-postgres   # cluster-admin required
```

```yaml
apiVersion: apps/v1
kind: StatefulSet
metadata:
  name: asqs-postgres
spec:
  serviceName: asqs-postgres
  replicas: 1
  selector: { matchLabels: { app: asqs-postgres } }
  template:
    metadata: { labels: { app: asqs-postgres } }
    spec:
      serviceAccountName: asqs-postgres
      containers:
        - name: postgres
          image: pgvector/pgvector:pg16
          ports: [ { containerPort: 5432 } ]
          env:
            - name: POSTGRES_DB
              value: asqs
            - name: POSTGRES_USER
              valueFrom: { secretKeyRef: { name: asqs-db, key: username } }
            - name: POSTGRES_PASSWORD
              valueFrom: { secretKeyRef: { name: asqs-db, key: password } }
            - name: PGDATA
              value: /var/lib/postgresql/data/pgdata
          volumeMounts:
            - { name: data, mountPath: /var/lib/postgresql/data }
          readinessProbe:
            exec: { command: ["pg_isready", "-U", "asqs", "-d", "asqs"] }
            initialDelaySeconds: 10
          resources:
            requests: { cpu: "500m", memory: 1Gi }
            limits:   { cpu: "2",    memory: 4Gi }
  volumeClaimTemplates:
    - metadata: { name: data }
      spec:
        accessModes: [ ReadWriteOnce ]
        resources: { requests: { storage: 20Gi } }
---
apiVersion: v1
kind: Service
metadata: { name: asqs-postgres }
spec:
  selector: { app: asqs-postgres }
  ports: [ { port: 5432, targetPort: 5432 } ]
```

Connection string for the config: `postgres://asqs:<pw>@asqs-postgres.asqs.svc:5432/asqs?sslmode=disable`
(use `sslmode=require` for an external managed DB).

### 4.3 Verify

```bash
oc run pgcheck --rm -it --restart=Never --image=registry.redhat.io/rhel9/postgresql-16 -- \
  psql "postgres://asqs:<pw>@asqs-postgres.asqs.svc:5432/asqs" \
  -c "CREATE EXTENSION IF NOT EXISTS vector; SELECT extversion FROM pg_extension WHERE extname='vector';"
```

After the first asqs-core run, `\dt` should show `symbols`, `edges`, `files`, `index_runs`,
and `chunks`.

---

## 5. Configuration and secrets

Split the config: **non-secret YAML in a ConfigMap, credentials in a Secret**, and let the
env-var overrides fill in the secrets. asqs-core supports env overrides on config fields (the
`env:` struct tags in `internal/config/config.go`), and the config's `*_from_env` keys let you
name the variable holding an API key.

### 5.1 ConfigMap

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: asqs-core-config
data:
  config.yaml: |
    schema_version: 2

    general:
      database:
        metadata_url: ""          # injected via env (see Secret below)
        embeddings_url: ""        # empty = same as metadata

      llm:
        provider: openai
        api_key_from_env: OPENAI_API_KEY
        model: gpt-4o
        max_concurrent: 4
        http:
          timeout: "10m"
        embeddings:
          model: text-embedding-3-small
          dimension: 1536         # 768 for Ollama nomic-embed-text — set before first run

      build:
        build_tool: ""            # auto: detects Maven or Gradle from the repository

      sandbox:
        type: local               # see section 2 — the OpenShift default
        timeout: "30m"

      # One block for the ACTIVE provider. v1 had four near-identical vcs.<provider> sections and
      # read only one of them; fill this in for whichever provider you set above.
      git:
        provider: bitbucket
        token: ""                 # injected via env as ASQS_GIT_TOKEN
        base_url: ""              # Server/DC: https://bitbucket.example.com/rest/api/1.0
        default_owner: ""         # Bitbucket workspace (Cloud) or project key (Server)
        default_repo: ""

    bootstrap:
      test_framework:
        enabled: false
        execution: local          # never "docker" on OpenShift
      e2e_framework:
        enabled: false
        execution: local

    indexer:
      type: advanced
      execution: local            # toolchains come from the image, not containers
      java:
        advanced_jar_path: "/opt/asqs/tools/java-indexer/target/java-indexer-0.1.0.jar"
      jsts:
        indexer_path: "/opt/asqs/tools/js-ts-indexer/dist/index.js"
      policy:
        max_gaps: 10
        max_gaps_per_file: 2

    fixer:
      iterations:
        start: 3
        max: 10
```

### 5.2 Secret

```bash
oc create secret generic asqs-core-secrets \
  --from-literal=ASQS_DATABASE_METADATA_URL='postgres://asqs:<pw>@asqs-postgres.asqs.svc:5432/asqs?sslmode=disable' \
  --from-literal=OPENAI_API_KEY='<llm-key>' \
  --from-literal=ASQS_BITBUCKET_TOKEN='<bitbucket-token>'
```

Confirm the exact env-var names against the `env:` tags in `internal/config/config.go` for
your revision of asqs-core before relying on them — they are the contract, and this is a
fast-moving file. A single run with a deliberately wrong DB password is a cheap way to prove
the variable is being read.

> **Do not copy variable names between asqs-core and asqs-go.** asqs-go moved to a restructured
> configuration schema in which the four provider tokens collapsed into one — `ASQS_GIT_TOKEN`,
> with `ASQS_GIT_AZURE_DEVOPS_TOKEN` for the cross-provider Azure PAT — and several keys changed
> section. asqs-core still uses the names above and has not been ported. The two products no longer
> share config files or variable names; treat each document as authoritative only for its own
> binary.

> **Bitbucket note:** asqs-core sends the token as `Authorization: Bearer`. On Bitbucket Cloud
> use a workspace/repository **Access Token** or OAuth token — a classic app password uses
> Basic auth and will 401.

---

## 6. Running a one-shot Job

```yaml
apiVersion: batch/v1
kind: Job
metadata:
  generateName: asqs-core-run-
spec:
  backoffLimit: 0                 # generation is expensive; don't silently retry
  ttlSecondsAfterFinished: 86400  # keep logs for a day, then clean up
  activeDeadlineSeconds: 7200     # hard wall-clock cap
  template:
    spec:
      restartPolicy: Never
      serviceAccountName: asqs-core
      automountServiceAccountToken: false     # the run needs no cluster API access
      securityContext:
        runAsNonRoot: true
        seccompProfile: { type: RuntimeDefault }
      containers:
        - name: asqs-core
          image: image-registry.openshift-image-registry.svc:5000/asqs/asqs-core:latest
          args:
            - "run"
            - "--config=/etc/asqs/config.yaml"
            - "--repo=https://bitbucket.example.com/scm/team/service-a.git"
            - "--max-gaps=10"
            - "--docs"
            # - "--ship"            # only when the pod carries a write-scoped VCS token
          envFrom:
            - secretRef: { name: asqs-core-secrets }
          env:
            - name: TMPDIR
              value: /workspace/tmp
          securityContext:
            allowPrivilegeEscalation: false
            capabilities: { drop: ["ALL"] }
            readOnlyRootFilesystem: false      # toolchains write in-tree; see note
          volumeMounts:
            - { name: config,    mountPath: /etc/asqs, readOnly: true }
            - { name: workspace, mountPath: /workspace }
            - { name: caches,    mountPath: /home/asqs }
          resources:
            requests: { cpu: "1",  memory: 4Gi }
            limits:   { cpu: "4",  memory: 8Gi }
      volumes:
        - name: config
          configMap: { name: asqs-core-config }
        - name: workspace
          emptyDir: { sizeLimit: 20Gi }        # clone + build output; ephemeral per run
        - name: caches
          persistentVolumeClaim: { claimName: asqs-core-caches }   # ~/.m2, ~/.npm — see 6.1
```

Create the supporting objects once:

```bash
oc create sa asqs-core
oc apply -f pvc-caches.yaml     # RWO 20Gi, mounted at /home/asqs
oc create -f job.yaml           # `create`, not `apply` — generateName needs it
oc logs -f job/<name>
```

Nothing here needs an SCC beyond the default `restricted-v2`.

### 6.1 The dependency cache PVC

Maven/npm re-downloading the world on every run is the single biggest avoidable cost. Mount a
PVC at `/home/asqs` so `~/.m2`, `~/.npm`, and `~/.gradle` survive between runs.

Caveat: a `ReadWriteOnce` PVC binds to one node and **serializes concurrent Jobs**. If you run
several repos in parallel, either use a `ReadWriteMany` storage class, or give each repo its
own PVC and select it per Job, or accept the cold cache and drop the volume.

### 6.2 On `readOnlyRootFilesystem`

Set to `false` above because the build toolchains write inside the repo tree and into various
tool-specific locations. Hardening it to `true` is possible but requires `emptyDir` mounts at
every path the toolchains touch (`/tmp`, `/home/asqs`, `/workspace`, plus wherever Maven and
npm scratch) — worth doing if policy demands it, but budget time for iterating on the failures.

---

## 7. Recurring runs (CronJob)

Since asqs-core has no built-in scheduler daemon, the CronJob **is** the schedule.

```yaml
apiVersion: batch/v1
kind: CronJob
metadata:
  name: asqs-core-nightly-service-a
spec:
  schedule: "0 1 * * *"            # 01:00 — cluster timezone (UTC unless timeZone is set)
  timeZone: "Europe/Berlin"        # OpenShift 4.13+ / k8s 1.27+
  concurrencyPolicy: Forbid        # never overlap runs on the same repo
  successfulJobsHistoryLimit: 3
  failedJobsHistoryLimit: 3
  startingDeadlineSeconds: 3600
  jobTemplate:
    spec:
      backoffLimit: 0
      activeDeadlineSeconds: 7200
      template:
        spec:
          # …identical to the Job pod spec in section 6…
```

Run **one CronJob per repository**, staggering the schedules so they don't contend for the
cache PVC or your LLM rate limit.

---

## 8. If you must nest containers

Only pursue these after reading section 2. Both require a custom SCC from your platform team.

### 8.1 DinD sidecar (requires the `privileged` SCC)

```bash
oc create sa asqs-core-dind
oc adm policy add-scc-to-user privileged -z asqs-core-dind   # cluster-admin required
```

```yaml
    spec:
      serviceAccountName: asqs-core-dind
      containers:
        - name: asqs-core
          image: …/asqs-core:latest
          env:
            - { name: DOCKER_HOST,  value: "tcp://localhost:2375" }
            - { name: DOCKER_TLS_CERTDIR, value: "" }
          volumeMounts:
            - { name: workspace, mountPath: /workspace }    # SAME PATH as the sidecar
            - { name: config, mountPath: /etc/asqs, readOnly: true }
        - name: dind
          image: docker.io/library/docker:27-dind
          securityContext:
            privileged: true
          env:
            - { name: DOCKER_TLS_CERTDIR, value: "" }
          volumeMounts:
            - { name: workspace,  mountPath: /workspace }   # SAME PATH as asqs-core
            - { name: dind-store, mountPath: /var/lib/docker }
      volumes:
        - { name: workspace,  emptyDir: { sizeLimit: 20Gi } }
        - { name: dind-store, emptyDir: { sizeLimit: 30Gi } }
        - { name: config, configMap: { name: asqs-core-config } }
```

**The critical detail:** the daemon lives in the `dind` container, so when asqs-core issues
`docker run -v /workspace/repo:/workspace`, that source path is resolved **in the sidecar's
filesystem**. The shared `workspace` volume must therefore be mounted at the **identical path
in both containers** — otherwise eval containers start against an empty directory and every
compile fails with "no pom.xml / package.json found". The same applies to any cache directory
you pass through `cache_maven_host` and friends: it must exist in the sidecar too.

Keep `general.sandbox.type: docker` and leave `docker_binary` at its default.

### 8.2 Rootless Podman in-pod (requires a custom SCC)

Add `podman` and `fuse-overlayfs` to the image, then:

```yaml
          env:
            - { name: RUNNER_DOCKER_BINARY, value: "podman" }   # or runner.docker_binary in YAML
```

Ask your platform team for an SCC granting a user namespace with a mapped UID range plus
`/dev/fuse`; the exact shape depends on your OpenShift version, so treat the specifics as a
conversation with them rather than a copy-paste. The upside over 8.1: Podman runs in the same
container as asqs-core, so bind-mount paths are naturally consistent and there is no sidecar
to keep in sync.

---

## 9. Networking: proxy, registries, egress

### 9.1 Cluster proxy

If the cluster has a `Proxy` object configured, OpenShift injects `HTTP_PROXY`,
`HTTPS_PROXY`, and `NO_PROXY` into pods automatically. Make sure `NO_PROXY` covers the
in-cluster DB (`.svc`, `.cluster.local`) and the internal Bitbucket host. Add the trusted CA
bundle when the proxy does TLS interception, otherwise the LLM and git calls fail with x509
errors:

```yaml
# In the pod spec
          volumeMounts:
            - { name: trusted-ca, mountPath: /etc/pki/ca-trust/extracted/pem, readOnly: true }
      volumes:
        - name: trusted-ca
          configMap:
            name: trusted-ca            # created with the inject label, see below
            items: [ { key: ca-bundle.crt, path: tls-ca-bundle.pem } ]
```

```bash
oc create configmap trusted-ca
oc label configmap trusted-ca config.openshift.io/inject-trusted-cabundle=true
```

### 9.2 Private Maven / npm registries

In `local` mode the toolchains read their normal config files, so mount them from a Secret:
a `settings.xml` at `$MAVEN_CONFIG/settings.xml` (`/home/asqs/.m2/settings.xml`) and an
`.npmrc` at `$HOME/.npmrc`. Note these paths sit inside the cache PVC — mount the Secret at a
separate path and copy, or bake a `settings.xml` that reads credentials from env, since a
Secret volume and a PVC cannot both own `/home/asqs`.

(The `general.sandbox.registries.credentials` mechanism in the commercial `asqs-go` injects these
into *containers*; in asqs-core `local` mode you provide them the ordinary way.)

### 9.3 Egress control

A NetworkPolicy recovers some of the isolation you lose by not using `--network none`. Allow
only what a run needs — DNS, the DB, the LLM endpoint, and the git host — and deny the rest.
If your cluster uses OVN-Kubernetes, an `EgressFirewall` gives you the same control at the
namespace level with CIDR/DNS rules.

---

## 10. Troubleshooting

| Symptom | Cause / fix |
| --- | --- |
| Pod fails: `container has runAsNonRoot and image has non-numeric user` | The Dockerfile ends with `USER asqs`. Use the numeric form (`USER 1001`). |
| `permission denied` creating `~/.m2` / `.npm` | `HOME` points at a non-writable path, or image dirs aren't group-0 writable. Set `HOME` and apply `chgrp -R 0 && chmod -R g=u` (section 3.1). |
| Postgres pod `CrashLoopBackOff` with a `chown`/permission error | Community Postgres image under `restricted-v2`. Use an operator, or grant `anyuid` to the DB service account (section 4.1). |
| `could not open extension control file "vector.control"` | pgvector isn't installed **on the server**. With an external DB this is a DBA task; with the community image it's built in. |
| Vector dimension errors after switching embedding models | `embeddings_dimension` is applied when the schema is **created**. Changing it later needs a re-index against a clean chunks table. |
| Eval fails: `docker: command not found` | `general.sandbox.type` is still `docker` but there's no container engine in the pod. Set `general.sandbox.type: local` (or `--sandbox local`). |
| Eval containers start but find no source (`no pom.xml`) | DinD sidecar path mismatch — the workspace volume must be mounted at the **same path** in both containers (section 8.1). |
| `exec: "sh": executable file not found` | A custom `compile_command`/`test_command`/`format_command` runs via `sh -c` (`internal/runner/local.go:236`). UBI includes `sh`; if you moved to a distroless base, either add a shell or drop the overrides. |
| Job killed at exactly the deadline | `activeDeadlineSeconds` is shorter than the run. Large repos with a fixer loop can exceed an hour — raise it and `general.sandbox.timeout` together. |
| Pod OOMKilled during index or eval | Raise the memory limit. JVM-based indexing and Maven builds are the usual culprits; also consider `-XX:MaxRAMPercentage` in `MAVEN_OPTS`. |
| Runs are slow and always re-download dependencies | The cache PVC isn't mounted at `HOME`, or a second Job is holding the RWO volume (section 6.1). |
| x509 / TLS errors to the LLM or git host | Trusted CA bundle not injected (section 9.1). |
| LLM calls time out behind the proxy | Raise `general.llm.http.timeout`; for slow local models also raise the response-header timeout if your revision exposes it. |

---

## 11. Sizing

- **Run pod:** start at `requests: 1 CPU / 4 Gi`, `limits: 4 CPU / 8 Gi`. Java repos with
  Maven builds are the heaviest; JS/TS indexing of a large Angular app is memory-hungry in the
  Node indexer.
- **Workspace `emptyDir`:** 20 Gi covers a clone plus build output for most repos. Node
  projects with large `node_modules` push this up — check against your biggest target repo.
- **Cache PVC:** 20 Gi for `~/.m2` + `~/.npm` is a reasonable start; it grows slowly.
- **Postgres:** 2 CPU / 4 Gi and 20 Gi storage to begin. The `chunks` table with its HNSW
  index dominates growth — watch disk as you onboard repos.
- **Concurrency:** one Job per repo at a time (`concurrencyPolicy: Forbid`), and stagger
  CronJob schedules. `general.llm.max_concurrent` bounds in-flight LLM requests within a single run —
  keep it low (2–4) when several repos run in the same window, and lower still against a
  self-hosted model.
