/**
 * Heuristic HTTP client calls: fetch('/path'), axios.get('/path') → API_CLIENT_REQUEST + CALLS_API.
 * Optional test chains: $fetch/ofetch (Nuxt), supertest-style .get('/path') (first string arg is an API path).
 * Callee symbols are file-local; cross-file linking to server API_ROUTE uses path_pattern in retrieval.
 */

import { SyntaxKind } from "ts-morph";
import type { CallExpression, SourceFile } from "ts-morph";
import type { FileSymbolsEdges } from "./enrichers";
import { normalizeHttpPath } from "./enrichers";
import { fqName } from "./normalize";

function callerFqForCall(node: CallExpression, moduleFq: string): string {
  let p = node.getParent();
  while (p) {
    if (p.isKind(SyntaxKind.FunctionDeclaration)) {
      const n = p.asKindOrThrow(SyntaxKind.FunctionDeclaration).getName();
      if (n) return fqName(moduleFq, n);
    }
    if (p.isKind(SyntaxKind.MethodDeclaration)) {
      const md = p.asKindOrThrow(SyntaxKind.MethodDeclaration);
      const cls = md.getParentIfKind(SyntaxKind.ClassDeclaration);
      const mn = md.getName();
      const cname = cls?.getName();
      if (cname && mn) return fqName(moduleFq, `${cname}.${mn}`);
    }
    p = p.getParent();
  }
  return moduleFq;
}

function pushApiClientRequest(
  entry: FileSymbolsEdges,
  moduleFq: string,
  httpMethod: string,
  pathPattern: string,
  line: number,
  endLine: number,
  lib: "fetch" | "axios" | "ofetch" | "http_chain_test",
  callerFq: string,
): void {
  const pathNorm = normalizeHttpPath("", pathPattern);
  const symFq = `API_CLIENT_REQUEST:${httpMethod}:${pathNorm}@${moduleFq}:L${line}`;
  entry.symbols.push({
    kind: "API_CLIENT_REQUEST",
    fq_name: symFq,
    start_line: line,
    end_line: endLine,
    signature: {
      framework: lib,
      http_method: httpMethod,
      path_pattern: pathNorm,
    },
  });
  entry.edges.push({
    caller_fq_name: callerFq,
    callee_fq_name: symFq,
    edge_type: "CALLS_API",
  });
}

export type HttpClientEnrichOpts = {
  /** When true, also index $fetch/ofetch and .get('/api')-style chains (typical in E2E/integration tests). */
  allowTestChains?: boolean;
};

/**
 * Non-test TS/TSX: string-literal relative URLs only (starts with /).
 * With allowTestChains: test-side $fetch, ofetch, and property .get/.post…('/path').
 */
export function enrichFileHttpClient(
  sf: SourceFile,
  entry: FileSymbolsEdges,
  moduleFq: string,
  opts?: HttpClientEnrichOpts,
): void {
  sf.forEachDescendant((node) => {
    if (!node.isKind(SyntaxKind.CallExpression)) return;
    const call = node;
    const expr = call.getExpression();
    const args = call.getArguments();

    if (opts?.allowTestChains) {
      const et = expr.getText().replace(/\s/g, "");
      if ((et === "$fetch" || et === "ofetch") && args.length >= 1) {
        const a0 = args[0];
        if (a0.isKind(SyntaxKind.StringLiteral)) {
          const url = a0.getLiteralText();
          if (url.startsWith("/")) {
            const callerFq = callerFqForCall(call, moduleFq);
            pushApiClientRequest(
              entry,
              moduleFq,
              "GET",
              url,
              call.getStartLineNumber(),
              call.getEndLineNumber(),
              "ofetch",
              callerFq,
            );
          }
        }
      }
    }

    // fetch('/api/x')
    if (expr.getText().trim() === "fetch" && args.length >= 1) {
      const a0 = args[0];
      if (a0.isKind(SyntaxKind.StringLiteral)) {
        const url = a0.getLiteralText();
        if (url.startsWith("/")) {
          const callerFq = callerFqForCall(call, moduleFq);
          pushApiClientRequest(
            entry,
            moduleFq,
            "GET",
            url,
            call.getStartLineNumber(),
            call.getEndLineNumber(),
            "fetch",
            callerFq,
          );
        }
      }
      return;
    }

    // axios.get('/x') OR supertest-style request(app).get('/x') (allowTestChains).
    if (expr.isKind(SyntaxKind.PropertyAccessExpression)) {
      const pa = expr.asKindOrThrow(SyntaxKind.PropertyAccessExpression);
      const methodName = pa.getName().toLowerCase();
      if (!["get", "post", "put", "patch", "delete"].includes(methodName)) return;
      if (args.length < 1) return;
      const a0 = args[0];
      if (!a0.isKind(SyntaxKind.StringLiteral)) return;
      const pathVal = a0.getLiteralText();
      if (!pathVal.startsWith("/")) return;
      const target = pa.getExpression().getText().replace(/\s/g, "");
      const httpMethod = methodName === "get" ? "GET" : methodName.toUpperCase();
      const callerFq = callerFqForCall(call, moduleFq);
      if (target === "axios" || target.endsWith("axios")) {
        pushApiClientRequest(
          entry,
          moduleFq,
          httpMethod,
          pathVal,
          call.getStartLineNumber(),
          call.getEndLineNumber(),
          "axios",
          callerFq,
        );
        return;
      }
      if (opts?.allowTestChains) {
        pushApiClientRequest(
          entry,
          moduleFq,
          httpMethod,
          pathVal,
          call.getStartLineNumber(),
          call.getEndLineNumber(),
          "http_chain_test",
          callerFq,
        );
      }
      return;
    }
  });
}
