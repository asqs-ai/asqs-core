package com.example;

import java.io.IOException;
import java.nio.file.Files;
import java.nio.file.Path;
import java.nio.file.Paths;
import java.util.ArrayList;
import java.util.Arrays;
import java.util.List;
import java.util.Locale;
import java.util.regex.Matcher;
import java.util.regex.Matcher;
import java.util.regex.Pattern;
import java.util.Optional;
import java.util.concurrent.atomic.AtomicInteger;
import java.util.stream.Collectors;
import java.util.stream.Stream;

import com.fasterxml.jackson.databind.ObjectMapper;
import com.fasterxml.jackson.databind.node.ArrayNode;
import com.fasterxml.jackson.databind.node.ObjectNode;
import com.github.javaparser.ParserConfiguration;
import com.github.javaparser.StaticJavaParser;
import com.github.javaparser.ast.CompilationUnit;
import com.github.javaparser.ast.ImportDeclaration;
import com.github.javaparser.ast.body.ClassOrInterfaceDeclaration;
import com.github.javaparser.ast.body.ConstructorDeclaration;
import com.github.javaparser.ast.body.EnumConstantDeclaration;
import com.github.javaparser.ast.body.EnumDeclaration;
import com.github.javaparser.ast.body.FieldDeclaration;
import com.github.javaparser.ast.body.MethodDeclaration;
import com.github.javaparser.ast.body.Parameter;
import com.github.javaparser.ast.body.RecordDeclaration;
import com.github.javaparser.ast.expr.AnnotationExpr;
import com.github.javaparser.ast.expr.Expression;
import com.github.javaparser.ast.expr.FieldAccessExpr;
import com.github.javaparser.ast.expr.MemberValuePair;
import com.github.javaparser.ast.expr.NameExpr;
import com.github.javaparser.ast.expr.NormalAnnotationExpr;
import com.github.javaparser.ast.expr.SingleMemberAnnotationExpr;
import com.github.javaparser.ast.expr.StringLiteralExpr;
import com.github.javaparser.ast.expr.MethodCallExpr;
import com.github.javaparser.ast.nodeTypes.NodeWithJavadoc;
import com.github.javaparser.resolution.UnsolvedSymbolException;
import com.github.javaparser.symbolsolver.JavaSymbolSolver;
import com.github.javaparser.symbolsolver.resolution.typesolvers.CombinedTypeSolver;
import com.github.javaparser.symbolsolver.resolution.typesolvers.JavaParserTypeSolver;
import com.github.javaparser.symbolsolver.resolution.typesolvers.ReflectionTypeSolver;

/**
 * Advanced Java indexer aligned with multi-phase discovery + semantic graph:
 * <ul>
 * <li>Phase 1 — Repo discovery: build tool hints, Maven modules, Java file count (java_meta line)</li>
 * <li>Phase 2 — Semantic unit: package, imports, types, methods, constructors, fields, calls, Javadoc,
 * extends/implements</li>
 * <li>Phase 4 (Java) — Spring MVC: structured route hints for Go to emit API_ROUTE (same contract as minimal
 * spring_web)</li>
 * <li>E2E semantic indexing (parity with JS/TS): optional {@code java_file_enrichment} per Java source with
 * {@code E2E_SPEC}, {@code TEST_SELECTOR} (Playwright {@code getByTestId}), and {@code API_CLIENT_REQUEST}
 * (WebClient {@code .uri}, RestTemplate, {@code URI.create}, RestAssured/MockMvc-style {@code get("/path")}, …)</li>
 * <li>{@code java_html_hooks} lines for HTML under {@code src/main/resources} and {@code src/test/resources}:
 * {@code STATIC_TEMPLATE} / {@code UI_TEST_HOOK} ({@code data-testid}, {@code data-cy}, Thymeleaf
 * {@code th:data-testid}, {@code th:testid})</li>
 * </ul>
 * One JSONL record per class/interface/record/enum, plus at most one file-level enrichment line. Go aggregates by
 * filePath into ParsedFile.
 */
public class JavaIndexer {

    private static final ObjectMapper M = new ObjectMapper();
    private static final Pattern MAVEN_MODULE = Pattern.compile("<module>\\s*([^<]+?)\\s*</module>");
    private static final Pattern HTML_DATA_TESTID =
            Pattern.compile("data-testid\\s*=\\s*[\"']([^\"']+)[\"']", Pattern.CASE_INSENSITIVE);
    private static final Pattern HTML_DATA_CY =
            Pattern.compile("data-cy\\s*=\\s*[\"']([^\"']+)[\"']", Pattern.CASE_INSENSITIVE);
    private static final Pattern TH_DATA_TESTID =
            Pattern.compile("th:data-testid\\s*=\\s*[\"']([^\"']+)[\"']", Pattern.CASE_INSENSITIVE);
    private static final Pattern TH_TESTID =
            Pattern.compile("th:testid\\s*=\\s*[\"']([^\"']+)[\"']", Pattern.CASE_INSENSITIVE);

    public static void main(String[] args) throws Exception {
        if (args.length < 1) {
            System.err.println("Usage: java -jar java-indexer.jar /path/to/repo");
            System.exit(2);
        }
        Path repo = Paths.get(args[0]).toAbsolutePath().normalize();
        if (!Files.isDirectory(repo)) {
            System.err.println("Not a directory: " + repo);
            System.exit(2);
        }

        CombinedTypeSolver typeSolver = new CombinedTypeSolver();
        typeSolver.add(new ReflectionTypeSolver());
        typeSolver.add(new JavaParserTypeSolver(repo));

        JavaSymbolSolver symbolSolver = new JavaSymbolSolver(typeSolver);
        // Default parser language level is below modern Java; without this, sources using pattern instanceof,
        // records, text blocks, etc. fail with e.g. "Use of patterns with instanceof is not supported."
        StaticJavaParser.getConfiguration()
                .setLanguageLevel(ParserConfiguration.LanguageLevel.JAVA_17)
                .setSymbolResolver(symbolSolver);

        List<Path> javaFiles = new ArrayList<>();
        try (Stream<Path> files = Files.walk(repo)) {
            files.filter(p -> Files.isRegularFile(p) && p.toString().endsWith(".java"))
                    .filter(p -> !shouldSkipPath(repo, p))
                    .forEach(javaFiles::add);
        }

        emitJavaMetaDiscovery(repo, javaFiles.size());

        for (Path p : javaFiles) {
            try {
                indexFile(repo, p);
            } catch (Exception e) {
                System.err.println("WARN: failed to index " + p + " : " + e.getMessage());
            }
        }
        try {
            emitHtmlTemplateHooks(repo);
        } catch (Exception e) {
            System.err.println("WARN: html template hooks: " + e.getMessage());
        }
    }

    private static boolean shouldSkipPath(Path repoRoot, Path file) {
        Path rel = repoRoot.relativize(file);
        for (int i = 0; i < rel.getNameCount(); i++) {
            String seg = rel.getName(i).toString().toLowerCase(Locale.ROOT);
            if (seg.equals("target")
                    || seg.equals("build")
                    || seg.equals(".git")
                    || seg.equals("node_modules")
                    || seg.equals(".gradle")
                    || seg.equals("out")
                    || seg.equals("dist")) {
                return true;
            }
        }
        return false;
    }

    /** Phase 1 — single JSONL line (skipped by Go file aggregation). */
    private static void emitJavaMetaDiscovery(Path repoRoot, int javaFileCount) {
        try {
            AtomicInteger pomCount = new AtomicInteger();
            AtomicInteger gradleCount = new AtomicInteger();
            try (Stream<Path> walk = Files.walk(repoRoot, 8)) {
                walk.filter(Files::isRegularFile)
                        .forEach(
                                p -> {
                                    String name = p.getFileName().toString();
                                    if ("pom.xml".equals(name)) {
                                        pomCount.incrementAndGet();
                                    } else if ("build.gradle".equals(name) || "build.gradle.kts".equals(name)) {
                                        gradleCount.incrementAndGet();
                                    }
                                });
            }

            List<String> mavenModules = new ArrayList<>();
            Path rootPom = repoRoot.resolve("pom.xml");
            if (Files.isRegularFile(rootPom)) {
                String xml = Files.readString(rootPom);
                Matcher m = MAVEN_MODULE.matcher(xml);
                while (m.find()) {
                    mavenModules.add(m.group(1).trim());
                }
            }

            int poms = pomCount.get();
            int gradles = gradleCount.get();
            String buildTool = "unknown";
            if (poms > 0 && gradles > 0) {
                buildTool = "maven+gradle";
            } else if (poms > 0) {
                buildTool = "maven";
            } else if (gradles > 0) {
                buildTool = "gradle";
            }

            ObjectNode node = M.createObjectNode();
            node.put("kind", "java_meta");
            node.put("schemaVersion", 1);
            node.put("phase", "discovery");
            node.put("repoRoot", repoRoot.toString());
            node.put("buildTool", buildTool);
            node.put("pomCount", poms);
            node.put("gradleCount", gradles);
            node.put("javaFileCount", javaFileCount);
            ArrayNode mods = M.createArrayNode();
            for (String mod : mavenModules) {
                mods.add(mod);
            }
            node.set("mavenRootModules", mods);
            System.out.println(M.writeValueAsString(node));
        } catch (Exception e) {
            System.err.println("WARN: java_meta discovery failed: " + e.getMessage());
        }
    }

    private static void indexFile(Path repoRoot, Path file) throws IOException {
        CompilationUnit cu = StaticJavaParser.parse(file);
        String filePath = repoRoot.relativize(file).toString().replace('\\', '/');

        String packageName = cu.getPackageDeclaration().map(pd -> pd.getNameAsString()).orElse("");

        cu.findAll(ClassOrInterfaceDeclaration.class).forEach(c -> {
            if (c.isNestedType()) {
                return;
            }
            try {
                emitClassLikeSymbol(c, filePath, packageName, cu, c.isInterface() ? "interface" : "class");
            } catch (Exception e) {
                System.err.println("WARN: class emit failed " + c.getName() + " : " + e.getMessage());
            }
        });

        cu.findAll(RecordDeclaration.class).forEach(r -> {
            if (r.isNestedType()) {
                return;
            }
            try {
                emitRecordSymbol(r, filePath, packageName, cu);
            } catch (Exception ex) {
                System.err.println("WARN: record emit failed " + r.getName() + " : " + ex.getMessage());
            }
        });

        cu.findAll(EnumDeclaration.class).forEach(e -> {
            if (e.isNestedType()) {
                return;
            }
            try {
                emitEnumSymbol(e, filePath, packageName);
            } catch (Exception ex) {
                System.err.println("WARN: enum emit failed " + e.getName() + " : " + ex.getMessage());
            }
        });

        try {
            emitFileEnrichment(repoRoot, file, cu, filePath, packageName);
        } catch (Exception ex) {
            System.err.println("WARN: file enrichment failed " + filePath + " : " + ex.getMessage());
        }
    }

    private static void emitClassLikeSymbol(
            ClassOrInterfaceDeclaration c,
            String filePath,
            String packageName,
            CompilationUnit cu,
            String kind) {
        String fqName = c.getFullyQualifiedName().orElse(c.getNameAsString());
        ObjectNode node = M.createObjectNode();
        node.put("id", "java:" + kind + ":" + fqName);
        node.put("kind", kind);
        node.put("fqName", fqName);
        node.put("filePath", filePath);
        node.put("packageName", packageName);
        node.put("isTest", looksLikeTestClass(c.getNameAsString(), filePath));

        node.put("startLine", lineOf(c.getRange()));
        node.put("endLine", endLineOf(c.getRange()));
        c.getRange()
                .ifPresent(
                        r -> {
                            node.put("startColumn", r.begin.column);
                            node.put("endColumn", r.end.column);
                        });
        node.put("javadocSummary", javadocSummary(c));

        ArrayNode imports = M.createArrayNode();
        for (ImportDeclaration im : cu.getImports()) {
            imports.add(im.toString().trim());
        }
        node.set("imports", imports);

        ArrayNode ext = M.createArrayNode();
        if (!c.isInterface()) {
            for (var et : c.getExtendedTypes()) {
                ext.add(resolveTypeName(et));
            }
        } else {
            for (var et : c.getExtendedTypes()) {
                ext.add(resolveTypeName(et));
            }
        }
        node.set("extendsTypes", ext);

        ArrayNode impl = M.createArrayNode();
        for (var it : c.getImplementedTypes()) {
            impl.add(resolveTypeName(it));
        }
        node.set("implementsTypes", impl);

        ArrayNode fieldsDetail = M.createArrayNode();
        for (FieldDeclaration fd : c.getFields()) {
            int fl = lineOf(fd.getRange());
            fd.getVariables()
                    .forEach(
                            v -> {
                                ObjectNode fn = M.createObjectNode();
                                fn.put("name", v.getNameAsString());
                                fn.put("type", fd.getElementType().asString());
                                fn.put("line", fl);
                                fd.getRange()
                                        .ifPresent(
                                                r -> {
                                                    fn.put("startColumn", r.begin.column);
                                                    fn.put("endColumn", r.end.column);
                                                });
                                fieldsDetail.add(fn);
                            });
        }
        node.set("fieldDetails", fieldsDetail);

        List<String> fields = new ArrayList<>();
        for (FieldDeclaration fd : c.getFields()) {
            fd.getVariables().forEach(v -> fields.add(v.getNameAsString() + ":" + fd.getElementType().asString()));
        }
        node.putPOJO("fields", fields);

        List<ObjectNode> methods = new ArrayList<>();
        for (ConstructorDeclaration ctor : c.getConstructors()) {
            methods.add(emitConstructor(ctor, fqName));
        }
        for (MethodDeclaration m : c.getMethods()) {
            methods.add(emitMethod(c, m, fqName));
        }
        node.putPOJO("methods", methods);

        List<String> annotations = new ArrayList<>();
        c.getAnnotations().forEach(a -> annotations.add(annotationToString(a)));
        node.putPOJO("annotations", annotations);
        node.putPOJO("diInjectedTypes", collectDIInjectedTypes(c));
        node.putPOJO("diRegisteredServices", collectDIRegisteredServices(c, packageName, fqName));
        node.putPOJO("diImplementsServices", collectDIImplementsServices(c));

        node.set("springRoutes", extractSpringRoutes(c));

        try {
            System.out.println(M.writeValueAsString(node));
        } catch (Exception e) {
            System.err.println("ERR: cannot write json for class " + fqName + " : " + e.getMessage());
        }
    }

    private static void emitRecordSymbol(RecordDeclaration r, String filePath, String packageName, CompilationUnit cu) {
        String fqName = r.getFullyQualifiedName().orElse(r.getNameAsString());
        ObjectNode node = M.createObjectNode();
        node.put("id", "java:record:" + fqName);
        node.put("kind", "record");
        node.put("fqName", fqName);
        node.put("filePath", filePath);
        node.put("packageName", packageName);
        node.put("isTest", looksLikeTestClass(r.getNameAsString(), filePath));

        node.put("startLine", lineOf(r.getRange()));
        node.put("endLine", endLineOf(r.getRange()));
        r.getRange()
                .ifPresent(
                        rng -> {
                            node.put("startColumn", rng.begin.column);
                            node.put("endColumn", rng.end.column);
                        });
        node.put("javadocSummary", javadocSummary(r));

        ArrayNode imports = M.createArrayNode();
        for (ImportDeclaration im : cu.getImports()) {
            imports.add(im.toString().trim());
        }
        node.set("imports", imports);

        ArrayNode impl = M.createArrayNode();
        for (var it : r.getImplementedTypes()) {
            impl.add(resolveTypeName(it));
        }
        node.set("extendsTypes", M.createArrayNode());
        node.set("implementsTypes", impl);

        ArrayNode fieldsDetail = M.createArrayNode();
        r.getParameters()
                .forEach(
                        p -> {
                            ObjectNode fn = M.createObjectNode();
                            fn.put("name", p.getNameAsString());
                            fn.put("type", p.getType().asString());
                            fn.put("line", lineOf(p.getRange()));
                            p.getRange()
                                    .ifPresent(
                                            rng -> {
                                                fn.put("startColumn", rng.begin.column);
                                                fn.put("endColumn", rng.end.column);
                                            });
                            fieldsDetail.add(fn);
                        });
        node.set("fieldDetails", fieldsDetail);

        List<String> fields = new ArrayList<>();
        r.getParameters().forEach(p -> fields.add(p.getNameAsString() + ":" + p.getType().asString()));
        node.putPOJO("fields", fields);

        List<ObjectNode> methods = new ArrayList<>();
        for (ConstructorDeclaration ctor : r.getConstructors()) {
            methods.add(emitConstructor(ctor, fqName));
        }
        for (MethodDeclaration m : r.getMethods()) {
            methods.add(emitMethod(r, m, fqName));
        }
        node.putPOJO("methods", methods);

        List<String> annotations = new ArrayList<>();
        r.getAnnotations().forEach(a -> annotations.add(annotationToString(a)));
        node.putPOJO("annotations", annotations);
        node.putPOJO("diInjectedTypes", collectDIInjectedTypes(r));
        node.putPOJO("diRegisteredServices", collectDIRegisteredServices(r, packageName, fqName));
        node.putPOJO("diImplementsServices", collectDIImplementsServices(r));

        node.set("springRoutes", extractSpringRoutesFromBody(r));

        try {
            System.out.println(M.writeValueAsString(node));
        } catch (Exception e) {
            System.err.println("ERR: cannot write json for record " + fqName + " : " + e.getMessage());
        }
    }

    private static ObjectNode emitConstructor(ConstructorDeclaration ctor, String classFq) {
        ObjectNode mnode = M.createObjectNode();
        String sig = constructorSignature(classFq, ctor);
        String ctorName = ctor.getNameAsString();
        String methodFq = classFq + "#" + ctorName;
        mnode.put("id", "java:ctor:" + sig);
        mnode.put("fqName", methodFq);
        mnode.put("name", ctorName);
        mnode.put("kind", "constructor");
        mnode.put("signature", ctor.getDeclarationAsString(false, false, true));
        mnode.put("startLine", lineOf(ctor.getRange()));
        mnode.put("endLine", endLineOf(ctor.getRange()));
        ctor.getRange()
                .ifPresent(
                        rng -> {
                            mnode.put("startColumn", rng.begin.column);
                            mnode.put("endColumn", rng.end.column);
                        });
        mnode.put("visibility", ctor.getAccessSpecifier().asString());
        mnode.put("isStatic", false);
        List<String> calls = collectCalls(ctor);
        mnode.putPOJO("calls", calls);
        return mnode;
    }

    private static ObjectNode emitMethod(ClassOrInterfaceDeclaration c, MethodDeclaration m, String classFq) {
        return emitMethodCommon(m, classFq, (body) -> collectCalls(body));
    }

    private static ObjectNode emitMethod(RecordDeclaration r, MethodDeclaration m, String classFq) {
        return emitMethodCommon(m, classFq, (body) -> collectCalls(body));
    }

    @FunctionalInterface
    private interface CallCollector {
        List<String> collect(com.github.javaparser.ast.body.BodyDeclaration<?> body);
    }

    private static ObjectNode emitMethodCommon(MethodDeclaration m, String classFq, CallCollector calls) {
        ObjectNode mnode = M.createObjectNode();
        String sig = methodSignatureFromName(classFq, m.getNameAsString(), m);
        mnode.put("id", "java:method:" + sig);
        String methodFq = classFq + "#" + m.getNameAsString();
        mnode.put("fqName", methodFq);
        mnode.put("name", m.getNameAsString());
        mnode.put("kind", "method");
        mnode.put("signature", m.getDeclarationAsString(false, false, true));
        mnode.put("startLine", lineOf(m.getRange()));
        mnode.put("endLine", endLineOf(m.getRange()));
        m.getRange()
                .ifPresent(
                        rng -> {
                            mnode.put("startColumn", rng.begin.column);
                            mnode.put("endColumn", rng.end.column);
                        });
        mnode.put("visibility", m.getAccessSpecifier().asString());
        mnode.put("isStatic", m.isStatic());
        mnode.putPOJO("calls", calls.collect(m));
        return mnode;
    }

    private static List<String> collectCalls(com.github.javaparser.ast.body.BodyDeclaration<?> body) {
        List<String> calls = new ArrayList<>();
        body.findAll(MethodCallExpr.class)
                .forEach(
                        call -> {
                            try {
                                calls.add(call.resolve().getQualifiedSignature());
                            } catch (UnsolvedSymbolException use) {
                                calls.add(
                                        "UNRESOLVED:"
                                                + call.getNameAsString()
                                                + "#"
                                                + call.getArguments().size());
                            } catch (Exception e) {
                                calls.add("UNRESOLVED:" + call.getNameAsString());
                            }
                        });
        return calls;
    }

    private static String constructorSignature(String classFq, ConstructorDeclaration ctor) {
        StringBuilder sig = new StringBuilder();
        sig.append(classFq).append("#<init>(");
        boolean first = true;
        for (Parameter p : ctor.getParameters()) {
            if (!first) {
                sig.append(",");
            }
            sig.append(p.getType().asString());
            first = false;
        }
        sig.append(")");
        return sig.toString();
    }

    private static String methodSignatureFromName(String classFq, String name, MethodDeclaration m) {
        StringBuilder sig = new StringBuilder();
        sig.append(classFq).append("#").append(name).append("(");
        boolean first = true;
        for (Parameter p : m.getParameters()) {
            if (!first) {
                sig.append(",");
            }
            sig.append(p.getType().asString());
            first = false;
        }
        sig.append(")");
        return sig.toString();
    }

    private static void emitEnumSymbol(EnumDeclaration e, String filePath, String packageName) {
        String fqName = e.getFullyQualifiedName().orElse(e.getNameAsString());
        ObjectNode node = M.createObjectNode();
        node.put("id", "java:enum:" + fqName);
        node.put("kind", "enum");
        node.put("fqName", fqName);
        node.put("filePath", filePath);
        node.put("packageName", packageName);
        node.put("isTest", looksLikeTestClass(e.getNameAsString(), filePath));
        node.put("startLine", lineOf(e.getRange()));
        node.put("endLine", endLineOf(e.getRange()));
        e.getRange()
                .ifPresent(
                        rng -> {
                            node.put("startColumn", rng.begin.column);
                            node.put("endColumn", rng.end.column);
                        });
        node.put("javadocSummary", javadocSummary(e));

        List<String> annotations = new ArrayList<>();
        e.getAnnotations().forEach(a -> annotations.add(annotationToString(a)));
        node.putPOJO("annotations", annotations);

        node.putPOJO("members", e.getEntries().stream().map(EnumConstantDeclaration::getNameAsString).toList());

        try {
            System.out.println(M.writeValueAsString(node));
        } catch (Exception ex) {
            System.err.println("ERR: cannot write json for enum " + fqName + " : " + ex.getMessage());
        }
    }

    private static ArrayNode extractSpringRoutes(ClassOrInterfaceDeclaration c) {
        if (!isSpringStereotypeController(c)) {
            return M.createArrayNode();
        }
        String classMapping = classLevelRequestMapping(c.getAnnotations());
        ArrayNode arr = M.createArrayNode();
        for (MethodDeclaration m : c.getMethods()) {
            SpringMapping sm = methodSpringMapping(m);
            if (sm == null) {
                continue;
            }
            ObjectNode route = M.createObjectNode();
            route.put("httpMethod", sm.httpMethod);
            route.put("classMapping", classMapping != null ? classMapping : "");
            route.put("methodMapping", sm.path != null ? sm.path : "");
            route.put("handlerMethod", m.getNameAsString());
            route.put("line", lineOf(m.getRange()));
            m.getRange()
                    .ifPresent(
                            rng -> {
                                route.put("startColumn", rng.begin.column);
                                route.put("endColumn", rng.end.column);
                            });
            arr.add(route);
        }
        return arr;
    }

    private static ArrayNode extractSpringRoutesFromBody(RecordDeclaration r) {
        if (!isSpringStereotypeController(r)) {
            return M.createArrayNode();
        }
        String classMapping = classLevelRequestMapping(r.getAnnotations());
        ArrayNode arr = M.createArrayNode();
        for (MethodDeclaration m : r.getMethods()) {
            SpringMapping sm = methodSpringMapping(m);
            if (sm == null) {
                continue;
            }
            ObjectNode route = M.createObjectNode();
            route.put("httpMethod", sm.httpMethod);
            route.put("classMapping", classMapping != null ? classMapping : "");
            route.put("methodMapping", sm.path != null ? sm.path : "");
            route.put("handlerMethod", m.getNameAsString());
            route.put("line", lineOf(m.getRange()));
            m.getRange()
                    .ifPresent(
                            rng -> {
                                route.put("startColumn", rng.begin.column);
                                route.put("endColumn", rng.end.column);
                            });
            arr.add(route);
        }
        return arr;
    }

    private static boolean isSpringStereotypeController(ClassOrInterfaceDeclaration c) {
        for (AnnotationExpr a : c.getAnnotations()) {
            String simple = a.getName().getIdentifier();
            if ("RestController".equals(simple)) {
                return true;
            }
            if ("Controller".equals(simple)) {
                return true;
            }
        }
        return false;
    }

    private static boolean isSpringStereotypeController(RecordDeclaration r) {
        for (AnnotationExpr a : r.getAnnotations()) {
            String simple = a.getName().getIdentifier();
            if ("RestController".equals(simple)) {
                return true;
            }
            if ("Controller".equals(simple)) {
                return true;
            }
        }
        return false;
    }

    private static String classLevelRequestMapping(List<AnnotationExpr> annotations) {
        for (AnnotationExpr a : annotations) {
            String simple = a.getName().getIdentifier();
            if ("RequestMapping".equals(simple)) {
                return extractPathFromMappingAnnotation(a).orElse("");
            }
        }
        return "";
    }

    private static SpringMapping methodSpringMapping(MethodDeclaration m) {
        for (AnnotationExpr a : m.getAnnotations()) {
            String simple = a.getName().getIdentifier();
            switch (simple) {
                case "GetMapping":
                    return new SpringMapping("GET", extractPathFromMappingAnnotation(a).orElse(""));
                case "PostMapping":
                    return new SpringMapping("POST", extractPathFromMappingAnnotation(a).orElse(""));
                case "PutMapping":
                    return new SpringMapping("PUT", extractPathFromMappingAnnotation(a).orElse(""));
                case "PatchMapping":
                    return new SpringMapping("PATCH", extractPathFromMappingAnnotation(a).orElse(""));
                case "DeleteMapping":
                    return new SpringMapping("DELETE", extractPathFromMappingAnnotation(a).orElse(""));
                case "RequestMapping":
                    {
                        String path = extractPathFromMappingAnnotation(a).orElse("");
                        String method = "GET";
                        if (a.isNormalAnnotationExpr()) {
                            for (MemberValuePair p : a.asNormalAnnotationExpr().getPairs()) {
                                if ("method".equals(p.getNameAsString())) {
                                    String v = p.getValue().toString();
                                    if (v.contains("POST")) {
                                        method = "POST";
                                    } else if (v.contains("PUT")) {
                                        method = "PUT";
                                    } else if (v.contains("DELETE")) {
                                        method = "DELETE";
                                    } else if (v.contains("PATCH")) {
                                        method = "PATCH";
                                    }
                                }
                            }
                        }
                        return new SpringMapping(method, path);
                    }
                default:
                    break;
            }
        }
        return null;
    }

    private static Optional<String> extractPathFromMappingAnnotation(AnnotationExpr a) {
        if (a.isMarkerAnnotationExpr()) {
            return Optional.of("");
        }
        if (a.isSingleMemberAnnotationExpr()) {
            return literalString(a.asSingleMemberAnnotationExpr().getMemberValue());
        }
        if (a.isNormalAnnotationExpr()) {
            NormalAnnotationExpr n = a.asNormalAnnotationExpr();
            for (MemberValuePair p : n.getPairs()) {
                String pn = p.getNameAsString();
                if ("value".equals(pn) || "path".equals(pn)) {
                    Optional<String> s = literalString(p.getValue());
                    if (s.isPresent()) {
                        return s;
                    }
                }
            }
        }
        return Optional.of("");
    }

    private static Optional<String> literalString(Expression expr) {
        if (expr instanceof StringLiteralExpr sle) {
            return Optional.of(sle.getValue());
        }
        String t = expr.toString();
        if (t.length() >= 2 && t.startsWith("\"") && t.endsWith("\"")) {
            return Optional.of(t.substring(1, t.length() - 1));
        }
        return Optional.empty();
    }

    private static final class SpringMapping {
        final String httpMethod;
        final String path;

        SpringMapping(String httpMethod, String path) {
            this.httpMethod = httpMethod;
            this.path = path;
        }
    }

    private static String resolveTypeName(com.github.javaparser.ast.type.Type t) {
        try {
            return t.resolve().describe();
        } catch (Exception e) {
            return t.asString();
        }
    }

    private static String javadocSummary(NodeWithJavadoc<?> node) {
        return node.getJavadocComment()
                .map(
                        jc -> {
                            String s = jc.getContent();
                            int nl = s.indexOf('\n');
                            String first = nl > 0 ? s.substring(0, nl) : s;
                            first = first.replace('*', ' ').trim();
                            return first.length() > 512 ? first.substring(0, 512) : first;
                        })
                .orElse("");
    }

    private static String annotationToString(AnnotationExpr a) {
        return a.toString().replaceAll("\\s+", " ").trim();
    }

    private static List<String> collectDIInjectedTypes(ClassOrInterfaceDeclaration c) {
        List<String> out = new ArrayList<>();
        for (ConstructorDeclaration ctor : c.getConstructors()) {
            for (Parameter p : ctor.getParameters()) {
                String t = resolveTypeName(p.getType());
                if (t != null && !t.isBlank()) {
                    out.add(t.trim());
                }
            }
        }
        return out.stream().distinct().toList();
    }

    private static List<String> collectDIInjectedTypes(RecordDeclaration r) {
        List<String> out = new ArrayList<>();
        for (Parameter p : r.getParameters()) {
            String t = resolveTypeName(p.getType());
            if (t != null && !t.isBlank()) {
                out.add(t.trim());
            }
        }
        for (ConstructorDeclaration ctor : r.getConstructors()) {
            for (Parameter p : ctor.getParameters()) {
                String t = resolveTypeName(p.getType());
                if (t != null && !t.isBlank()) {
                    out.add(t.trim());
                }
            }
        }
        return out.stream().distinct().toList();
    }

    private static List<String> collectDIRegisteredServices(
            ClassOrInterfaceDeclaration c, String packageName, String fqName) {
        List<String> out = new ArrayList<>();
        if (isSpringServiceStereotype(c.getAnnotations())) {
            out.add(fqName);
        }
        if (hasAnnotation(c.getAnnotations(), "Configuration")) {
            for (MethodDeclaration m : c.getMethods()) {
                if (!hasAnnotation(m.getAnnotations(), "Bean")) {
                    continue;
                }
                String ret = resolveTypeName(m.getType());
                if (ret == null || ret.isBlank()) {
                    continue;
                }
                String r = ret.trim();
                switch (r.toLowerCase(Locale.ROOT)) {
                    case "void":
                    case "int":
                    case "long":
                    case "short":
                    case "byte":
                    case "double":
                    case "float":
                    case "boolean":
                    case "char":
                        continue;
                    default:
                        out.add(r);
                }
            }
        }
        return out.stream().distinct().toList();
    }

    private static List<String> collectDIRegisteredServices(
            RecordDeclaration r, String packageName, String fqName) {
        List<String> out = new ArrayList<>();
        if (isSpringServiceStereotype(r.getAnnotations())) {
            out.add(fqName);
        }
        if (hasAnnotation(r.getAnnotations(), "Configuration")) {
            for (MethodDeclaration m : r.getMethods()) {
                if (!hasAnnotation(m.getAnnotations(), "Bean")) {
                    continue;
                }
                String ret = resolveTypeName(m.getType());
                if (ret == null || ret.isBlank()) {
                    continue;
                }
                String rr = ret.trim();
                switch (rr.toLowerCase(Locale.ROOT)) {
                    case "void":
                    case "int":
                    case "long":
                    case "short":
                    case "byte":
                    case "double":
                    case "float":
                    case "boolean":
                    case "char":
                        continue;
                    default:
                        out.add(rr);
                }
            }
        }
        return out.stream().distinct().toList();
    }

    private static List<String> collectDIImplementsServices(ClassOrInterfaceDeclaration c) {
        if (!isSpringServiceStereotype(c.getAnnotations())) {
            return List.of();
        }
        return c.getImplementedTypes().stream()
                .map(JavaIndexer::resolveTypeName)
                .filter(s -> s != null && !s.isBlank())
                .map(String::trim)
                .distinct()
                .toList();
    }

    private static List<String> collectDIImplementsServices(RecordDeclaration r) {
        if (!isSpringServiceStereotype(r.getAnnotations())) {
            return List.of();
        }
        return r.getImplementedTypes().stream()
                .map(JavaIndexer::resolveTypeName)
                .filter(s -> s != null && !s.isBlank())
                .map(String::trim)
                .distinct()
                .toList();
    }

    private static boolean isSpringServiceStereotype(List<AnnotationExpr> annotations) {
        return hasAnnotation(annotations, "Service")
                || hasAnnotation(annotations, "Component")
                || hasAnnotation(annotations, "Repository")
                || hasAnnotation(annotations, "Controller")
                || hasAnnotation(annotations, "RestController");
    }

    private static boolean hasAnnotation(List<AnnotationExpr> annotations, String simpleName) {
        for (AnnotationExpr a : annotations) {
            String id = a.getName().getIdentifier();
            if (simpleName.equals(id)) {
                return true;
            }
            String fq = a.getNameAsString();
            if (fq.endsWith("." + simpleName)) {
                return true;
            }
        }
        return false;
    }

    private static int lineOf(Optional<com.github.javaparser.Range> range) {
        return range.map(r -> r.begin.line).orElse(-1);
    }

    private static int endLineOf(Optional<com.github.javaparser.Range> range) {
        return range.map(r -> r.end.line).orElse(-1);
    }

    private static boolean looksLikeTestClass(String className, String filePath) {
        String lower = className.toLowerCase(Locale.ROOT);
        if (lower.endsWith("test") || lower.endsWith("tests")) {
            return true;
        }
        String fp = filePath.replace('\\', '/').toLowerCase(Locale.ROOT);
        return fp.contains("/test/")
                || fp.contains("/tests/")
                || fp.contains("/it/")
                || fp.endsWith("tests.java");
    }

    /** True if path looks like an E2E spec (mirrors JS isLikelyE2ESpecPath where applicable). */
    private static boolean isLikelyJavaE2EPath(String filePath) {
        String p = filePath.replace('\\', '/').toLowerCase(Locale.ROOT);
        if (p.contains("/e2e/")) {
            return true;
        }
        if (p.contains("playwright")) {
            return true;
        }
        if (p.matches("(?i).+\\.e2e\\.java$")) {
            return true;
        }
        if (p.contains("/it/") && p.contains("/test/")) {
            return true;
        }
        return false;
    }

    private static boolean looksLikeJavaTestFilePath(String filePath) {
        String fp = filePath.replace('\\', '/').toLowerCase(Locale.ROOT);
        return fp.contains("/test/") || fp.contains("/tests/") || fp.contains("/it/");
    }

    /** playwright_java | selenium_java | junit_e2e | unknown */
    private static String detectJavaE2EFramework(CompilationUnit cu) {
        boolean playwright = false;
        boolean selenium = false;
        boolean junit = false;
        for (ImportDeclaration im : cu.getImports()) {
            String nm = im.getNameAsString();
            if (nm.startsWith("com.microsoft.playwright")) {
                playwright = true;
            }
            if (nm.startsWith("org.openqa.selenium")) {
                selenium = true;
            }
            if (nm.startsWith("org.junit.jupiter") || nm.startsWith("org.junit.")) {
                junit = true;
            }
        }
        if (playwright) {
            return "playwright_java";
        }
        if (selenium) {
            return "selenium_java";
        }
        if (junit) {
            return "junit_e2e";
        }
        return "unknown";
    }

    private static Optional<com.github.javaparser.Range> findFirstJUnitTestMethodRange(CompilationUnit cu) {
        for (MethodDeclaration m : cu.findAll(MethodDeclaration.class)) {
            if (m.isAnnotationPresent("Test")) {
                return m.getRange();
            }
            for (AnnotationExpr a : m.getAnnotations()) {
                if ("Test".equals(a.getName().getIdentifier())) {
                    return m.getRange();
                }
            }
        }
        return Optional.empty();
    }

    private static Optional<String> methodCallerFq(MethodCallExpr call) {
        Optional<MethodDeclaration> md = call.findAncestor(MethodDeclaration.class);
        if (md.isEmpty()) {
            return Optional.empty();
        }
        Optional<ClassOrInterfaceDeclaration> cd = md.get().findAncestor(ClassOrInterfaceDeclaration.class);
        if (cd.isPresent()) {
            ClassOrInterfaceDeclaration c = cd.get();
            String classFq = c.getFullyQualifiedName().orElse(c.getNameAsString());
            return Optional.of(classFq + "#" + md.get().getNameAsString());
        }
        Optional<RecordDeclaration> rd = md.get().findAncestor(RecordDeclaration.class);
        if (rd.isPresent()) {
            RecordDeclaration r = rd.get();
            String classFq = r.getFullyQualifiedName().orElse(r.getNameAsString());
            return Optional.of(classFq + "#" + md.get().getNameAsString());
        }
        return Optional.empty();
    }

    private static String httpMethodFromWebClientChain(MethodCallExpr uriCall) {
        Optional<Expression> scopeOpt = uriCall.getScope();
        if (scopeOpt.isEmpty() || !(scopeOpt.get() instanceof MethodCallExpr)) {
            return "GET";
        }
        MethodCallExpr inner = (MethodCallExpr) scopeOpt.get();
        String n = inner.getNameAsString().toLowerCase(Locale.ROOT);
        return switch (n) {
            case "post" -> "POST";
            case "put" -> "PUT";
            case "patch" -> "PATCH";
            case "delete" -> "DELETE";
            default -> "GET";
        };
    }

    private static void emitFileEnrichment(
            Path repoRoot, Path file, CompilationUnit cu, String filePath, String packageName) throws IOException {
        boolean testFile = looksLikeJavaTestFilePath(filePath);
        ArrayNode selectors = M.createArrayNode();
        ObjectNode e2eBlock = null;

        if (testFile && isLikelyJavaE2EPath(filePath)) {
            String fw = detectJavaE2EFramework(cu);
            if (!"unknown".equals(fw)) {
                Optional<com.github.javaparser.Range> tr = findFirstJUnitTestMethodRange(cu);
                if (tr.isPresent()) {
                    e2eBlock = M.createObjectNode();
                    e2eBlock.put("startLine", tr.get().begin.line);
                    e2eBlock.put("endLine", tr.get().end.line);
                    e2eBlock.put("framework", fw);
                }
            }
            if ("playwright_java".equals(fw)) {
                for (MethodCallExpr mc : cu.findAll(MethodCallExpr.class)) {
                    if (!"getByTestId".equals(mc.getNameAsString()) || mc.getArguments().isEmpty()) {
                        continue;
                    }
                    Expression a0 = mc.getArgument(0);
                    Optional<String> tid = literalString(a0);
                    if (tid.isEmpty()) {
                        continue;
                    }
                    ObjectNode sel = M.createObjectNode();
                    sel.put("testId", tid.get());
                    sel.put("line", lineOf(mc.getRange()));
                    sel.put("endLine", endLineOf(mc.getRange()));
                    selectors.add(sel);
                }
            }
        }

        // Extract HTTP client calls from both main and test code so integration/E2E tests (WebTestClient,
        // RestTemplate in @SpringBootTest) emit API_CLIENT_REQUEST → TARGETS_API_ROUTE and coverage can be inferred.
        ArrayNode apiCalls = M.createArrayNode();
        {
            for (MethodCallExpr mc : cu.findAll(MethodCallExpr.class)) {
                String name = mc.getNameAsString();
                // WebClient: .uri("/api/...")
                if ("uri".equals(name) && !mc.getArguments().isEmpty()) {
                    Optional<String> path = literalString(mc.getArgument(0));
                    if (path.isPresent() && path.get().startsWith("/")) {
                        String httpMethod = httpMethodFromWebClientChain(mc);
                        Optional<String> caller = methodCallerFq(mc);
                        if (caller.isPresent()) {
                            ObjectNode o = M.createObjectNode();
                            o.put("httpMethod", httpMethod);
                            o.put("path", path.get());
                            o.put("line", lineOf(mc.getRange()));
                            o.put("endLine", endLineOf(mc.getRange()));
                            o.put("framework", "webclient");
                            o.put("callerMethodFq", caller.get());
                            apiCalls.add(o);
                        }
                    }
                    continue;
                }
                // RestTemplate: getForObject, postForObject, exchange — first arg string URL
                if (List.of("getForObject", "postForObject", "put", "patchForObject", "exchange")
                                .contains(name)
                        && !mc.getArguments().isEmpty()) {
                    Optional<String> url = literalString(mc.getArgument(0));
                    if (url.isEmpty() || (!url.get().startsWith("/") && !url.get().startsWith("http"))) {
                        continue;
                    }
                    String httpMethod =
                            switch (name) {
                                case "postForObject" -> "POST";
                                case "put" -> "PUT";
                                case "patchForObject" -> "PATCH";
                                case "exchange" -> "GET";
                                default -> "GET";
                            };
                    Optional<String> caller = methodCallerFq(mc);
                    if (caller.isEmpty()) {
                        continue;
                    }
                    String pathOnly = url.get();
                    if (pathOnly.startsWith("http")) {
                        int slash = pathOnly.indexOf('/', pathOnly.indexOf("://") + 3);
                        pathOnly = slash >= 0 ? pathOnly.substring(slash) : "/";
                    }
                    ObjectNode o = M.createObjectNode();
                    o.put("httpMethod", httpMethod);
                    o.put("path", pathOnly);
                    o.put("line", lineOf(mc.getRange()));
                    o.put("endLine", endLineOf(mc.getRange()));
                    o.put("framework", "rest_template");
                    o.put("callerMethodFq", caller.get());
                    apiCalls.add(o);
                }
                // java.net.URI.create("/api/...") — often passed to HttpRequest.newBuilder(...).uri(...)
                if ("create".equals(name) && !mc.getArguments().isEmpty() && mc.getScope().isPresent()) {
                    Expression sc = mc.getScope().get();
                    boolean uriType =
                            (sc instanceof NameExpr ne && "URI".equals(ne.getNameAsString()))
                                    || (sc instanceof FieldAccessExpr fa
                                            && "URI".equals(fa.getNameAsString()));
                    if (uriType) {
                        Optional<String> path = literalString(mc.getArgument(0));
                        if (path.isPresent()
                                && (path.get().startsWith("/") || path.get().startsWith("http"))) {
                            Optional<String> caller = methodCallerFq(mc);
                            if (caller.isPresent()) {
                                String pathOnly = path.get();
                                if (pathOnly.startsWith("http")) {
                                    int slash = pathOnly.indexOf('/', pathOnly.indexOf("://") + 3);
                                    pathOnly = slash >= 0 ? pathOnly.substring(slash) : "/";
                                }
                                ObjectNode o = M.createObjectNode();
                                o.put("httpMethod", "GET");
                                o.put("path", pathOnly);
                                o.put("line", lineOf(mc.getRange()));
                                o.put("endLine", endLineOf(mc.getRange()));
                                o.put("framework", "uri_create");
                                o.put("callerMethodFq", caller.get());
                                apiCalls.add(o);
                            }
                        }
                    }
                }
                // RestAssured: given().get("/api/x") — MockMvcRequestBuilders.get("/x") — same shape.
                String verb = name.toLowerCase(Locale.ROOT);
                if (Arrays.asList("get", "post", "put", "patch", "delete", "head").contains(verb)
                        && !mc.getArguments().isEmpty()) {
                    Optional<String> pathOpt = literalString(mc.getArgument(0));
                    if (pathOpt.isPresent()) {
                        String p = pathOpt.get();
                        if (p.startsWith("/") || p.startsWith("http")) {
                            Optional<String> caller = methodCallerFq(mc);
                            if (caller.isPresent()) {
                                String pathOnly = p;
                                if (pathOnly.startsWith("http")) {
                                    int slash = pathOnly.indexOf('/', pathOnly.indexOf("://") + 3);
                                    pathOnly = slash >= 0 ? pathOnly.substring(slash) : "/";
                                }
                                if (pathOnly.startsWith("/")) {
                                    String httpMethod =
                                            switch (verb) {
                                                case "post" -> "POST";
                                                case "put" -> "PUT";
                                                case "patch" -> "PATCH";
                                                case "delete" -> "DELETE";
                                                case "head" -> "HEAD";
                                                default -> "GET";
                                            };
                                    ObjectNode o = M.createObjectNode();
                                    o.put("httpMethod", httpMethod);
                                    o.put("path", pathOnly);
                                    o.put("line", lineOf(mc.getRange()));
                                    o.put("endLine", endLineOf(mc.getRange()));
                                    o.put("framework", guessVerbHttpFramework(cu, testFile));
                                    o.put("callerMethodFq", caller.get());
                                    apiCalls.add(o);
                                }
                            }
                        }
                    }
                }
            }
        }

        if (e2eBlock == null && selectors.isEmpty() && apiCalls.isEmpty()) {
            return;
        }

        ObjectNode root = M.createObjectNode();
        root.put("kind", "java_file_enrichment");
        root.put("filePath", filePath);
        root.put("packageName", packageName);
        root.put("isTest", testFile);
        if (e2eBlock != null) {
            root.set("e2eSpec", e2eBlock);
        }
        if (!selectors.isEmpty()) {
            root.set("testSelectors", selectors);
        }
        if (!apiCalls.isEmpty()) {
            root.set("apiClientRequests", apiCalls);
        }
        System.out.println(M.writeValueAsString(root));
    }

    private static boolean compilationUnitImportsPrefix(CompilationUnit cu, String prefix) {
        for (ImportDeclaration im : cu.getImports()) {
            String nm = im.getNameAsString();
            if (nm.startsWith(prefix)) {
                return true;
            }
        }
        return false;
    }

    private static String guessVerbHttpFramework(CompilationUnit cu, boolean testFile) {
        if (compilationUnitImportsPrefix(cu, "io.restassured")) {
            return "rest_assured";
        }
        if (compilationUnitImportsPrefix(cu, "org.springframework.test.web.servlet.request")) {
            return "mock_mvc";
        }
        if (testFile) {
            return "http_verb_test";
        }
        return "http_verb";
    }

    /** Emit java_html_hooks JSONL lines for Thymeleaf/static HTML testability attributes. */
    private static void emitHtmlTemplateHooks(Path repoRoot) throws IOException {
        Path[] bases =
                new Path[] {
                    repoRoot.resolve("src/main/resources"), repoRoot.resolve("src/test/resources")
                };
        for (Path base : bases) {
            if (!Files.isDirectory(base)) {
                continue;
            }
            try (Stream<Path> walk = Files.walk(base)) {
                List<Path> htmlFiles =
                        walk.filter(p -> Files.isRegularFile(p) && p.toString().endsWith(".html"))
                                .collect(Collectors.toList());
                for (Path p : htmlFiles) {
                    emitOneHtmlHooksFile(repoRoot, p);
                }
            }
        }
    }

    private static void emitOneHtmlHooksFile(Path repoRoot, Path absFile) throws IOException {
        String filePath = repoRoot.relativize(absFile).toString().replace('\\', '/');
        List<String> lines = Files.readAllLines(absFile);
        ArrayNode hooks = M.createArrayNode();
        for (int i = 0; i < lines.size(); i++) {
            String line = lines.get(i);
            int lineNum = i + 1;
            addHtmlHookMatches(line, lineNum, hooks, HTML_DATA_TESTID, "data-testid", "html");
            addHtmlHookMatches(line, lineNum, hooks, HTML_DATA_CY, "data-cy", "cypress_template");
            addHtmlHookMatches(line, lineNum, hooks, TH_DATA_TESTID, "data-testid", "thymeleaf");
            addHtmlHookMatches(line, lineNum, hooks, TH_TESTID, "testid", "thymeleaf");
        }
        if (hooks.isEmpty()) {
            return;
        }
        ObjectNode root = M.createObjectNode();
        root.put("kind", "java_html_hooks");
        root.put("filePath", filePath);
        root.set("hooks", hooks);
        System.out.println(M.writeValueAsString(root));
    }

    private static void addHtmlHookMatches(
            String line,
            int lineNum,
            ArrayNode hooks,
            Pattern pat,
            String selectorKind,
            String framework) {
        Matcher mat = pat.matcher(line);
        while (mat.find()) {
            String val = mat.group(1).trim();
            if (val.isEmpty()) {
                continue;
            }
            ObjectNode o = M.createObjectNode();
            o.put("line", lineNum);
            o.put("selectorKind", selectorKind);
            o.put("value", val);
            o.put("framework", framework);
            hooks.add(o);
        }
    }
}
