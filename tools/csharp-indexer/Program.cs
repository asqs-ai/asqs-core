using System.Collections.Immutable;
using System.Text.Json;
using System.Text.Json.Serialization;
using Basic.Reference.Assemblies;
using Microsoft.CodeAnalysis;
using Microsoft.CodeAnalysis.CSharp;
using Microsoft.CodeAnalysis.CSharp.Syntax;

namespace Asqs.CSharpIndexer;

internal static class Program
{
    private static readonly JsonSerializerOptions JsonOpts = new()
    {
        DefaultIgnoreCondition = JsonIgnoreCondition.WhenWritingNull,
        WriteIndented = false,
    };

    private static int Main(string[] args)
    {
        if (args.Length < 1 || string.IsNullOrWhiteSpace(args[0]))
        {
            Console.Error.WriteLine("Usage: CSharpIndexer <repo-root>");
            return 2;
        }

        var repo = Path.GetFullPath(args[0].Trim());
        if (!Directory.Exists(repo))
        {
            Console.Error.WriteLine($"Not a directory: {repo}");
            return 2;
        }

        // Precomputed net10.0 reference assemblies (TFM-specific package exposes Net100.References.All, not ReferenceAssemblies.Net100).
        var refs = Net100.References.All;
        var stats = new RunStats();

        // Spec 4(a)(c) / B24: one compilation PER PROJECT, not per file. A fresh single-file
        // compilation makes every sibling-file type an error symbol, so GetSymbolInfo returns null
        // and the CALLS edge silently disappears — in a layered project that is essentially all of
        // them, and cross-file semantics are the entire reason to pay for Roslyn. A project is the
        // directory tree rooted at a .csproj (nearest-ancestor ownership; nested projects own
        // their subtrees), compiled together with the sources of transitively ProjectReference'd
        // projects. Source inclusion instead of MSBuildWorkspace on purpose: no design-time build,
        // no NuGet restore, no network, and "a project that fails to load degrades to per-file"
        // stays a local catch instead of a workspace-wide failure mode.
        var files = EnumerateCsFiles(repo).OrderBy(f => f, StringComparer.Ordinal).ToList();
        var projectDirs = DiscoverProjectDirs(repo);
        var ownership = AssignOwnership(files, projectDirs);
        var refClosure = BuildProjectReferenceClosure(repo, projectDirs);
        var treeCache = new Dictionary<string, (SyntaxTree Tree, string Text)>(StringComparer.Ordinal);

        foreach (var projDir in projectDirs)
        {
            if (!ownership.OwnFiles.TryGetValue(projDir, out var ownFiles) || ownFiles.Count == 0)
            {
                continue;
            }
            stats.Projects++;
            try
            {
                IndexProject(repo, projDir, ownFiles, refClosure[projDir], ownership, refs, treeCache, stats);
            }
            catch (Exception ex)
            {
                var relDir = Rel(repo, projDir);
                Console.Error.WriteLine($"csharp-indexer: project {relDir} failed ({ex.Message}); falling back to per-file for {ownFiles.Count} file(s)");
                foreach (var f in ownFiles)
                {
                    IndexSingleFile(repo, f, refs, stats, countAsLoose: false);
                }
            }
        }
        foreach (var loose in ownership.LooseFiles)
        {
            IndexSingleFile(repo, loose, refs, stats, countAsLoose: true);
        }

        // Trailing summary object — no "path" key, so pre-B24 consumers skip it as a non-file
        // line. indexer.csharp_unresolved_invocations is the direct health metric for this change:
        // before it, a null symbol was a silent continue and the edge loss was invisible.
        var summary = new Dictionary<string, object>
        {
            ["summary"] = "csharp_indexer_run",
            ["projects"] = stats.Projects,
            ["project_files"] = stats.ProjectFiles,
            ["loose_files"] = stats.LooseFiles,
            ["invocations_resolved"] = stats.Resolved,
            ["invocations_unresolved"] = stats.Unresolved,
        };
        Console.WriteLine(JsonSerializer.Serialize(summary, JsonOpts));
        Console.Error.WriteLine($"csharp-indexer: {stats.Projects} project(s), {stats.ProjectFiles} project file(s), {stats.LooseFiles} loose file(s); invocations resolved={stats.Resolved} unresolved={stats.Unresolved}");

        return 0;
    }

    private sealed class RunStats
    {
        public int Projects;
        public int ProjectFiles;
        public int LooseFiles;
        public int Resolved;
        public int Unresolved;
    }

    private sealed class Ownership
    {
        public Dictionary<string, List<string>> OwnFiles { get; } = new(StringComparer.Ordinal);
        public List<string> LooseFiles { get; } = new();
    }

    private static string Rel(string repo, string path) => Path.GetRelativePath(repo, path).Replace('\\', '/');

    private static IEnumerable<string> EnumerateCsFiles(string repo)
    {
        foreach (var path in Directory.EnumerateFiles(repo, "*.cs", SearchOption.AllDirectories))
        {
            if (IsInSkippedDir(path)) continue;
            yield return path;
        }
    }

    private static readonly string[] SkippedDirSegments =
    {
        "bin", "obj", "out", "dist", "build", "target", "packages", "testresults",
        ".vs", ".git", ".idea", ".vscode", "node_modules",
    };

    // GeneratedFileSuffixes name files a tool wrote. Indexing them is worse than useless: the
    // symbols are real, so they compete for plan budget and prompt space with the hand-written code
    // the run exists to test, and nothing may edit them because the generator will overwrite them.
    private static readonly string[] GeneratedFileSuffixes =
    {
        ".designer.cs", ".g.cs", ".g.i.cs", ".generated.cs", "globalusings.g.cs", "assemblyinfo.cs",
    };

    private static bool IsInSkippedDir(string path)
    {
        var norm = path.Replace('\\', '/');
        foreach (var seg in SkippedDirSegments)
        {
            if (norm.Contains("/" + seg + "/", StringComparison.OrdinalIgnoreCase)) return true;
        }
        var name = Path.GetFileName(norm);
        foreach (var suffix in GeneratedFileSuffixes)
        {
            if (name.EndsWith(suffix, StringComparison.OrdinalIgnoreCase)) return true;
        }
        // An EF Core migration's designer file carries the model snapshot — thousands of lines of
        // generated builder calls that resolve to nothing a test can use.
        if (norm.Contains("/migrations/", StringComparison.OrdinalIgnoreCase)
            && name.EndsWith(".designer.cs", StringComparison.OrdinalIgnoreCase)) return true;
        return false;
    }

    // DiscoverProjectDirs returns the directories that contain at least one .csproj, sorted. The
    // DIRECTORY is the unit — a directory with two csprojs is one project group, which sidesteps
    // ambiguous file ownership between them.
    private static List<string> DiscoverProjectDirs(string repo)
    {
        var dirs = new SortedSet<string>(StringComparer.Ordinal);
        foreach (var csproj in Directory.EnumerateFiles(repo, "*.csproj", SearchOption.AllDirectories))
        {
            if (IsInSkippedDir(csproj)) continue;
            var dir = Path.GetDirectoryName(csproj);
            if (!string.IsNullOrEmpty(dir)) dirs.Add(Path.GetFullPath(dir));
        }
        return dirs.ToList();
    }

    // AssignOwnership maps each source file to its NEAREST ancestor project directory; files with
    // no project ancestor stay loose and keep the pre-B24 per-file behaviour.
    private static Ownership AssignOwnership(List<string> files, List<string> projectDirs)
    {
        var own = new Ownership();
        var dirSet = new HashSet<string>(projectDirs, StringComparer.Ordinal);
        foreach (var d in projectDirs) own.OwnFiles[d] = new List<string>();
        foreach (var file in files)
        {
            string? owner = null;
            var dir = Path.GetDirectoryName(Path.GetFullPath(file));
            while (!string.IsNullOrEmpty(dir))
            {
                if (dirSet.Contains(dir)) { owner = dir; break; }
                dir = Path.GetDirectoryName(dir);
            }
            if (owner != null) own.OwnFiles[owner].Add(file);
            else own.LooseFiles.Add(file);
        }
        return own;
    }

    // BuildProjectReferenceClosure parses each project directory's csprojs for ProjectReference
    // entries and returns the TRANSITIVE set of referenced project directories per project. A
    // malformed csproj or a reference outside the repo degrades to "no references", never to a
    // failed index.
    private static Dictionary<string, List<string>> BuildProjectReferenceClosure(string repo, List<string> projectDirs)
    {
        var dirSet = new HashSet<string>(projectDirs, StringComparer.Ordinal);
        var direct = new Dictionary<string, List<string>>(StringComparer.Ordinal);
        foreach (var dir in projectDirs)
        {
            var refsOut = new List<string>();
            foreach (var csproj in Directory.EnumerateFiles(dir, "*.csproj", SearchOption.TopDirectoryOnly))
            {
                foreach (var target in ParseProjectReferences(csproj))
                {
                    var targetDir = Path.GetDirectoryName(Path.GetFullPath(target));
                    if (string.IsNullOrEmpty(targetDir)) continue;
                    if (dirSet.Contains(targetDir) && targetDir != dir && !refsOut.Contains(targetDir))
                    {
                        refsOut.Add(targetDir);
                    }
                }
            }
            direct[dir] = refsOut;
        }

        var closure = new Dictionary<string, List<string>>(StringComparer.Ordinal);
        foreach (var dir in projectDirs)
        {
            var seen = new HashSet<string>(StringComparer.Ordinal) { dir };
            var queue = new Queue<string>(direct[dir]);
            var result = new List<string>();
            while (queue.Count > 0)
            {
                var next = queue.Dequeue();
                if (!seen.Add(next)) continue;
                result.Add(next);
                foreach (var r in direct.TryGetValue(next, out var rs) ? rs : new List<string>())
                {
                    queue.Enqueue(r);
                }
            }
            closure[dir] = result;
        }
        return closure;
    }

    private static IEnumerable<string> ParseProjectReferences(string csprojPath)
    {
        var result = new List<string>();
        try
        {
            var doc = System.Xml.Linq.XDocument.Load(csprojPath);
            foreach (var pr in doc.Descendants())
            {
                if (pr.Name.LocalName != "ProjectReference") continue;
                var include = pr.Attribute("Include")?.Value?.Trim();
                if (string.IsNullOrEmpty(include)) continue;
                var resolved = Path.Combine(Path.GetDirectoryName(csprojPath) ?? ".", include.Replace('\\', Path.DirectorySeparatorChar));
                result.Add(resolved);
            }
        }
        catch (Exception ex)
        {
            Console.Error.WriteLine($"csharp-indexer: csproj parse {csprojPath}: {ex.Message}");
        }
        return result;
    }

    // ImplicitUsingsTree synthesizes the base SDK global-usings set when any csproj in the group
    // enables ImplicitUsings — the real generated file lives under obj/ (excluded), and without it
    // simple names like Task or Console never bind. Base BCL set only: every namespace here exists
    // in the reference assemblies, so the synthetic tree can never introduce binding errors.
    private static SyntaxTree? ImplicitUsingsTree(string projDir)
    {
        var enabled = false;
        foreach (var csproj in Directory.EnumerateFiles(projDir, "*.csproj", SearchOption.TopDirectoryOnly))
        {
            try
            {
                var doc = System.Xml.Linq.XDocument.Load(csproj);
                foreach (var el in doc.Descendants())
                {
                    if (el.Name.LocalName == "ImplicitUsings" &&
                        (el.Value.Trim().Equals("enable", StringComparison.OrdinalIgnoreCase) ||
                         el.Value.Trim().Equals("true", StringComparison.OrdinalIgnoreCase)))
                    {
                        enabled = true;
                    }
                }
            }
            catch
            {
                // Malformed csproj already reported by ParseProjectReferences; no usings then.
            }
        }
        if (!enabled) return null;
        const string src = "global using System;\nglobal using System.Collections.Generic;\nglobal using System.IO;\nglobal using System.Linq;\nglobal using System.Net.Http;\nglobal using System.Threading;\nglobal using System.Threading.Tasks;\n";
        return CSharpSyntaxTree.ParseText(src, path: "__asqs_implicit_usings.cs");
    }

    private static (SyntaxTree Tree, string Text) ParseCached(
        string repo, string file, Dictionary<string, (SyntaxTree Tree, string Text)> cache)
    {
        var rel = Rel(repo, file);
        if (cache.TryGetValue(rel, out var hit)) return hit;
        var text = File.ReadAllText(file);
        var tree = CSharpSyntaxTree.ParseText(text, path: rel);
        var entry = (tree, text);
        cache[rel] = entry;
        return entry;
    }

    private static void IndexProject(
        string repo,
        string projDir,
        List<string> ownFiles,
        List<string> referencedDirs,
        Ownership ownership,
        IEnumerable<MetadataReference> refs,
        Dictionary<string, (SyntaxTree Tree, string Text)> treeCache,
        RunStats stats)
    {
        var trees = new List<SyntaxTree>();
        var ownEntries = new List<(string File, SyntaxTree Tree, string Text)>();
        foreach (var f in ownFiles)
        {
            var e = ParseCached(repo, f, treeCache);
            ownEntries.Add((f, e.Tree, e.Text));
            trees.Add(e.Tree);
        }
        // Referenced projects contribute their SOURCES so cross-project calls resolve; their files
        // are emitted only by their owning project, exactly once.
        foreach (var refDir in referencedDirs)
        {
            if (!ownership.OwnFiles.TryGetValue(refDir, out var refFiles)) continue;
            foreach (var f in refFiles)
            {
                trees.Add(ParseCached(repo, f, treeCache).Tree);
            }
        }
        if (ImplicitUsingsTree(projDir) is { } usingsTree)
        {
            trees.Add(usingsTree);
        }

        var asmName = "AsqsIndexer_" + new string(Rel(repo, projDir).Select(c => char.IsLetterOrDigit(c) ? c : '_').ToArray());
        var compilation = CSharpCompilation.Create(
            asmName,
            trees,
            refs,
            new CSharpCompilationOptions(OutputKind.DynamicallyLinkedLibrary,
                nullableContextOptions: NullableContextOptions.Enable));

        foreach (var (file, tree, text) in ownEntries)
        {
            var rel = Rel(repo, file);
            try
            {
                var model = compilation.GetSemanticModel(tree);
                var doc = IndexFile(rel, text, tree.GetRoot(), model, stats);
                Console.WriteLine(JsonSerializer.Serialize(doc, JsonOpts));
                stats.ProjectFiles++;
            }
            catch (Exception ex)
            {
                Console.Error.WriteLine($"csharp-indexer: skip {rel}: {ex.Message}");
            }
        }
    }

    // IndexSingleFile is the pre-B24 behaviour, kept verbatim for files no project owns and for
    // projects whose group compilation failed.
    private static void IndexSingleFile(string repo, string file, IEnumerable<MetadataReference> refs, RunStats stats, bool countAsLoose)
    {
        var rel = Rel(repo, file);
        try
        {
            var text = File.ReadAllText(file);
            var tree = CSharpSyntaxTree.ParseText(text, path: rel);
            var compilation = CSharpCompilation.Create(
                "AsqsIndexer_" + Guid.NewGuid().ToString("N"),
                new[] { tree },
                refs,
                new CSharpCompilationOptions(OutputKind.DynamicallyLinkedLibrary,
                    nullableContextOptions: NullableContextOptions.Enable));
            var model = compilation.GetSemanticModel(tree);
            var doc = IndexFile(rel, text, tree.GetRoot(), model, stats);
            Console.WriteLine(JsonSerializer.Serialize(doc, JsonOpts));
            if (countAsLoose) stats.LooseFiles++;
        }
        catch (Exception ex)
        {
            Console.Error.WriteLine($"csharp-indexer: skip {rel}: {ex.Message}");
        }
    }

    private sealed class InvocationStats
    {
        public int Resolved;
        public int Unresolved;
    }

    private static LangIndexerDoc IndexFile(string relPath, string text, SyntaxNode root, SemanticModel model, RunStats stats)
    {
        var invStats = new InvocationStats();
        var moduleNs = GuessModuleNamespace(root);
        var isTest = IsLikelyTestPath(relPath);
        var symbols = new List<SymbolDto>();
        var edges = new List<EdgeDto>();
        var edgeSeen = new HashSet<string>(StringComparer.Ordinal);

        void AddEdge(string callerFq, string calleeFq, string edgeType)
        {
            if (string.IsNullOrWhiteSpace(callerFq) || string.IsNullOrWhiteSpace(calleeFq))
            {
                return;
            }
            var canonical = CanonicalEdgeType(edgeType);
            if (string.IsNullOrWhiteSpace(canonical))
            {
                return;
            }
            var key = callerFq + "->" + calleeFq + ":" + canonical;
            if (!edgeSeen.Add(key))
            {
                return;
            }
            edges.Add(new EdgeDto
            {
                CallerFqName = callerFq,
                CalleeFqName = calleeFq,
                EdgeType = canonical,
            });
        }

        // A file with no namespace declaration still needs a module, or it has no container: every
        // CONTAINS edge is dropped, chunking has nothing to group by, and the file's symbols are
        // orphans. Top-level statements are the common case — Program.cs in every modern ASP.NET
        // template declares no namespace at all — and it is the entry point, so losing it loses the
        // one file that wires the application together.
        if (string.IsNullOrEmpty(moduleNs))
        {
            moduleNs = TopLevelModuleName(root);
        }
        if (!string.IsNullOrEmpty(moduleNs))
        {
            symbols.Add(new SymbolDto
            {
                Kind = "MODULE",
                FqName = moduleNs,
                StartLine = 1,
                EndLine = 1,
            });
        }

        foreach (var u in root.DescendantNodes().OfType<UsingDirectiveSyntax>())
        {
            var name = u.Name?.ToString().Trim();
            if (string.IsNullOrEmpty(name)) continue;
            if (string.IsNullOrEmpty(moduleNs)) continue;
            // `using static Xunit.Assert;` and `using Sut = Shop.Core.Basket;` both name a TYPE, so
            // the edge resolves to the type rather than to a namespace that does not exist. Skipping
            // the static form outright — which is what happened before — lost the import edge for
            // every file that gets its assertions that way, which in xUnit and NUnit is common.
            var isStatic = u.StaticKeyword.IsKind(SyntaxKind.StaticKeyword);
            var isAlias = u.Alias != null;
            if (isStatic || isAlias)
            {
                var target = model.GetTypeInfo(u.Name!).Type as INamedTypeSymbol;
                AddEdge(moduleNs, target != null ? TypeFqName(target) : name, "IMPORTS");
                continue;
            }
            AddEdge(moduleNs, name, "IMPORTS");
        }

        foreach (var typeDecl in root.DescendantNodes().OfType<TypeDeclarationSyntax>())
        {
            if (typeDecl.Identifier.Text.Length == 0) continue;
            var sym = model.GetDeclaredSymbol(typeDecl) as INamedTypeSymbol;
            if (sym == null) continue;

            var typeFq = TypeFqName(sym);
            var (sl, el, sc, ec) = LineSpan(typeDecl);

            var typeKind = typeDecl switch
            {
                InterfaceDeclarationSyntax => "interface",
                RecordDeclarationSyntax => "record",
                StructDeclarationSyntax => "struct",
                _ => "class",
            };

            symbols.Add(new SymbolDto
            {
                Kind = typeKind,
                FqName = typeFq,
                StartLine = sl,
                EndLine = el,
                StartColumn = sc,
                EndColumn = ec,
                Signature = BuildTypeSignature(sym, typeDecl),
            });
            if (!string.IsNullOrEmpty(moduleNs))
            {
                AddEdge(moduleNs, typeFq, "CONTAINS");
            }

            foreach (var bt in typeDecl.BaseList?.Types ?? default(SeparatedSyntaxList<BaseTypeSyntax>))
            {
                var baseSym = model.GetTypeInfo(bt.Type).Type as INamedTypeSymbol;
                var baseFq = baseSym != null ? TypeFqName(baseSym) : bt.Type.ToString().Trim();
                if (string.IsNullOrWhiteSpace(baseFq))
                {
                    continue;
                }
                if (typeDecl is InterfaceDeclarationSyntax)
                {
                    AddEdge(typeFq, baseFq, "EXTENDS");
                    continue;
                }
                if (baseSym != null && baseSym.TypeKind == TypeKind.Interface)
                {
                    AddEdge(typeFq, baseFq, "IMPLEMENTS");
                }
                else
                {
                    AddEdge(typeFq, baseFq, "EXTENDS");
                }
            }

            foreach (var m in typeDecl.Members)
            {
                switch (m)
                {
                    case MethodDeclarationSyntax md:
                        IndexMethod(md, typeFq, model, symbols, AddEdge);
                        ExtractAspNetRoutes(md, typeFq, model, symbols, edges);
                        ExtractMvcViewRoutes(md, typeFq, model, symbols, edges);
                        break;
                    case ConstructorDeclarationSyntax cd:
                        IndexConstructor(cd, typeFq, model, symbols, AddEdge);
                        break;
                    case FieldDeclarationSyntax fd:
                        IndexField(fd, typeFq, model, symbols, AddEdge);
                        break;
                    case PropertyDeclarationSyntax pd:
                        IndexProperty(pd, typeFq, model, symbols, AddEdge);
                        break;
                    case EventDeclarationSyntax ed:
                        IndexEvent(ed, typeFq, model, symbols, AddEdge);
                        break;
                    case EventFieldDeclarationSyntax efd:
                        IndexEventField(efd, typeFq, model, symbols, AddEdge);
                        break;
                    case IndexerDeclarationSyntax ixd:
                        IndexIndexer(ixd, typeFq, model, symbols, AddEdge);
                        break;
                    case OperatorDeclarationSyntax opd:
                        IndexOperator(opd, typeFq, model, symbols, AddEdge);
                        break;
                    case ConversionOperatorDeclarationSyntax cod:
                        IndexConversionOperator(cod, typeFq, model, symbols, AddEdge);
                        break;
                    case DelegateDeclarationSyntax dd:
                        IndexNestedDelegate(dd, typeFq, model, symbols, AddEdge);
                        break;
                }
            }

            // A record's positional parameters ARE its properties, and nothing in the body declares
            // them. Without this a record indexes as a type with no members at all, so every gap
            // against one is proposed blind and every member fact about one is empty.
            IndexRecordPositionalProperties(typeDecl, typeFq, model, symbols, AddEdge);

            // DI extraction: constructor injection edges for the most activatable constructor.
            EmitConstructorInjectionEdges(typeDecl, typeFq, model, AddEdge);

            ExtractRazorPageRoutes(typeDecl, tsymForRoutes(typeDecl, model), relPath, typeFq, model, symbols, edges);
        }

        // Once per compilation unit, not once per type. Called per type declaration this visited a
        // NESTED type twice — once inside its parent's descendants and once on its own — so its
        // invocations were counted twice in the resolution statistics. Scoping to the root also
        // reaches the invocations that live outside a type declaration's member list: property and
        // event accessor bodies, local functions, and a top-level-statement file's whole body.
        CollectInvocations(root, model, edges, invStats);

        // EnumDeclarationSyntax is a BaseTypeDeclarationSyntax and NOT a TypeDeclarationSyntax, so
        // the walk above never saw one: every enum in every C# repository indexed to nothing. A
        // switch over an enum is the single most common thing a generated test needs to enumerate.
        foreach (var enumDecl in root.DescendantNodes().OfType<EnumDeclarationSyntax>())
        {
            IndexEnum(enumDecl, moduleNs, model, symbols, AddEdge);
        }

        // A delegate declared at namespace level rather than inside a type.
        foreach (var dd in root.DescendantNodes().OfType<DelegateDeclarationSyntax>())
        {
            if (dd.Parent is TypeDeclarationSyntax) continue; // already emitted as a member
            IndexNestedDelegate(dd, moduleNs, model, symbols, AddEdge);
        }

        // E2E_SPEC. Three things were wrong at once: the FQ used a shape no other language emits,
        // only Playwright counted, and the span was the whole file.
        //
        // `relPath + "#e2e"` did not match the `E2E_SPEC:<path>` form Java and JS/TS produce, so
        // anything reading the prefix saw C# specs as a different kind of thing. Selenium and
        // WebApplicationFactory are the other two ways a .NET repository writes an end-to-end test,
        // and a repository using either had no E2E anchors at all — so the plan proposed E2E gaps
        // with nothing to model them on.
        var e2eFramework = DetectE2EFramework(text);
        if ((isTest || ContainsTestAttribute(text)) && e2eFramework != null)
        {
            // The first test method, not the whole file: a span covering every using directive and
            // every helper is a chunk the retrieval side cannot use as an example.
            var firstTest = root.DescendantNodes().OfType<MethodDeclarationSyntax>()
                .FirstOrDefault(m => ContainsTestAttribute(m.ToString()));
            var (sl, el, _, _) = LineSpan((SyntaxNode?)firstTest ?? root);
            symbols.Add(new SymbolDto
            {
                Kind = "E2E_SPEC",
                FqName = "E2E_SPEC:" + relPath,
                StartLine = sl,
                EndLine = el,
                Signature = JsonSerializer.SerializeToElement(new { framework = e2eFramework }),
            });
        }

        CollectTestSelectors(root, relPath, isTest || ContainsTestAttribute(text), symbols, edges);
        CollectHttpClientRequests(root, model, symbols, edges);
        CollectServiceRegistrationEdges(root, model, moduleNs, AddEdge);

        stats.Resolved += invStats.Resolved;
        stats.Unresolved += invStats.Unresolved;
        return new LangIndexerDoc
        {
            Path = relPath,
            Lang = "csharp",
            Module = moduleNs ?? "",
            IsTest = isTest,
            Symbols = symbols,
            Edges = edges,
            UnresolvedInvocations = invStats.Unresolved > 0 ? invStats.Unresolved : null,
        };
    }

    private static void IndexMethod(MethodDeclarationSyntax md, string typeFq, SemanticModel model,
        List<SymbolDto> symbols, Action<string, string, string> addEdge)
    {
        var ms = model.GetDeclaredSymbol(md);
        if (ms == null) return;
        var fq = MethodFqName(ms);
        var (sl, el, sc, ec) = LineSpan(md);
        symbols.Add(new SymbolDto
        {
            Kind = "method",
            FqName = fq,
            StartLine = sl,
            EndLine = el,
            StartColumn = sc,
            EndColumn = ec,
            Signature = BuildMethodSignature(ms, md),
        });
        addEdge(typeFq, fq, "CONTAINS");
        EmitCallableTypeSurfaceEdges(ms, fq, addEdge, includeReturnType: true);
        EmitFieldAccessEdges(md, model, fq, addEdge);
    }

    private static void IndexConstructor(ConstructorDeclarationSyntax cd, string typeFq, SemanticModel model,
        List<SymbolDto> symbols, Action<string, string, string> addEdge)
    {
        var cs = model.GetDeclaredSymbol(cd);
        if (cs == null) return;
        var fq = MethodFqName(cs);
        var (sl, el, sc, ec) = LineSpan(cd);
        symbols.Add(new SymbolDto
        {
            Kind = "constructor",
            FqName = fq,
            StartLine = sl,
            EndLine = el,
            StartColumn = sc,
            EndColumn = ec,
            Signature = BuildMethodSignature(cs, cd),
        });
        addEdge(typeFq, fq, "CONTAINS");
        EmitCallableTypeSurfaceEdges(cs, fq, addEdge, includeReturnType: false);
        EmitFieldAccessEdges(cd, model, fq, addEdge);
    }

    private static void IndexField(FieldDeclarationSyntax fd, string typeFq, SemanticModel model,
        List<SymbolDto> symbols, Action<string, string, string> addEdge)
    {
        foreach (var v in fd.Declaration.Variables)
        {
            var fs = model.GetDeclaredSymbol(v) as IFieldSymbol;
            if (fs == null) continue;
            var fq = typeFq + "#" + fs.Name;
            var (sl, el, sc, ec) = LineSpan(v);
            symbols.Add(new SymbolDto
            {
                Kind = "field",
                FqName = fq,
                StartLine = sl,
                EndLine = el,
                StartColumn = sc,
                EndColumn = ec,
                Signature = BuildMemberSignature(fs, fs.IsStatic),
            });
            addEdge(typeFq, fq, "CONTAINS");
        }
    }

    private static void CollectInvocations(SyntaxNode scope, SemanticModel model, List<EdgeDto> edges, InvocationStats invStats)
    {
        foreach (var inv in scope.DescendantNodes().OfType<InvocationExpressionSyntax>())
        {
            var callerMethodFq = FindEnclosingCallableFq(inv, model);
            if (string.IsNullOrEmpty(callerMethodFq)) continue;

            var sym = model.GetSymbolInfo(inv).Symbol;
            if (sym is IMethodSymbol called)
            {
                invStats.Resolved++;
                var callee = MethodFqName(called);
                if (!string.IsNullOrEmpty(callee))
                    edges.Add(new EdgeDto { CallerFqName = callerMethodFq, CalleeFqName = callee, EdgeType = "CALLS" });
            }
            else
            {
                // The silent-continue this counter replaces WAS the defect metric: before B24
                // every sibling-type call landed here invisibly.
                invStats.Unresolved++;
            }
        }
    }

    private static string? FindEnclosingCallableFq(SyntaxNode node, SemanticModel model)
    {
        foreach (var anc in node.Ancestors())
        {
            if (anc is MethodDeclarationSyntax md)
            {
                var s = model.GetDeclaredSymbol(md);
                return s != null ? MethodFqName(s) : null;
            }
            if (anc is ConstructorDeclarationSyntax cd)
            {
                var s = model.GetDeclaredSymbol(cd);
                return s != null ? MethodFqName(s) : null;
            }
        }
        return null;
    }

    // AspNetVerbAttributes maps a verb attribute to its method. [Route] on a method with no verb
    // attribute is a GET by ASP.NET's own convention, and [AcceptVerbs] carries its verbs as
    // arguments — both were dropped entirely, so any controller written that way had no routes at
    // all.
    private static readonly Dictionary<string, string> AspNetVerbAttributes = new(StringComparer.Ordinal)
    {
        ["HttpGetAttribute"] = "GET",
        ["HttpPostAttribute"] = "POST",
        ["HttpPutAttribute"] = "PUT",
        ["HttpDeleteAttribute"] = "DELETE",
        ["HttpPatchAttribute"] = "PATCH",
        ["HttpHeadAttribute"] = "HEAD",
        ["HttpOptionsAttribute"] = "OPTIONS",
    };

    // IsRoutableController gates route extraction on the type actually being a controller.
    //
    // Without it any class with a method called [HttpGet] emitted a route — including a test that
    // merely names the attribute. ASP.NET recognises a controller by ControllerBase ancestry or by
    // the [Controller] / [ApiController] attribute, and so does this.
    private static bool IsRoutableController(INamedTypeSymbol? t, TypeDeclarationSyntax? decl = null)
    {
        if (t == null && decl == null) return false;
        foreach (var entry in SyntaxAttributes(decl))
        {
            if (entry.Name is "ApiControllerAttribute" or "ControllerAttribute") return true;
            if (entry.Name == "NonControllerAttribute") return false;
        }
        if (t == null) return decl!.Identifier.Text.EndsWith("Controller", StringComparison.Ordinal);
        for (var cur = t.BaseType; cur != null; cur = cur.BaseType)
        {
            if (cur.Name is "ControllerBase" or "Controller") return true;
        }
        // Convention: a type whose name ends in Controller is one, which is how ASP.NET discovers
        // controllers that inherit from nothing.
        return t.Name.EndsWith("Controller", StringComparison.Ordinal);
    }

    // ExpandRouteTokens substitutes the three tokens ASP.NET replaces at startup.
    //
    // They were kept literally, so every attribute-routed controller in the language produced the
    // path "/api/[controller]" — a string no client call can ever match, which made every one of
    // those routes permanently uncovered and every TARGETS_API_ROUTE edge impossible.
    private static string ExpandRouteTokens(string template, INamedTypeSymbol? type, IMethodSymbol? method)
    {
        if (string.IsNullOrEmpty(template)) return template;
        if (type != null)
        {
            var controller = type.Name;
            if (controller.EndsWith("Controller", StringComparison.Ordinal) && controller.Length > "Controller".Length)
            {
                controller = controller[..^"Controller".Length];
            }
            template = ReplaceToken(template, "controller", controller);
            var area = AreaName(type);
            if (!string.IsNullOrEmpty(area)) template = ReplaceToken(template, "area", area);
        }
        if (method != null)
        {
            template = ReplaceToken(template, "action", method.Name);
        }
        return template;
    }

    private static string ReplaceToken(string template, string token, string value)
    {
        return System.Text.RegularExpressions.Regex.Replace(
            template, @"\[" + token + @"\]", value, System.Text.RegularExpressions.RegexOptions.IgnoreCase);
    }

    private static string AreaName(INamedTypeSymbol type)
    {
        foreach (var a in type.GetAttributes())
        {
            if (a.AttributeClass?.Name != "AreaAttribute") continue;
            foreach (var arg in a.ConstructorArguments)
            {
                if (arg.Value is string s && s.Length > 0) return s;
            }
        }
        return "";
    }

    // NormalizeRoutePath puts a route into the one shape both sides of a match can produce.
    //
    // A route template writes a parameter as `{id}` or `{id:int}` or `{*rest}`; a client call writes
    // a concrete value or an interpolation. Neither can be compared to the other as written, and
    // routeMatchKey on the Go side is an exact string compare — so every parameterised route was
    // unmatchable by construction. Both emitters here normalise a parameter segment to `*`, which
    // is the only comparison that can succeed.
    // NormalizeAttributeName maps an attribute to its TYPE name, whichever way it was written.
    //
    // This is load-bearing rather than cosmetic. The compilations this tool builds reference the
    // BCL and nothing else — no ASP.NET — so `[HttpGet]` does not resolve, and an unresolved
    // attribute's symbol carries the name AS WRITTEN: "HttpGet", never "HttpGetAttribute". Every
    // route rule keyed on the suffixed spelling, so in a real repository not one of them ever
    // matched: C# API routes have never been extracted, and everything built on them — uncovered
    // route gaps, TARGETS_API_ROUTE coverage, the E2E plan's route anchors — was dead for C#.
    private static string NormalizeAttributeName(string name)
    {
        name = name.Trim();
        var dot = name.LastIndexOf('.');
        if (dot >= 0) name = name[(dot + 1)..]; // Mvc.HttpGet -> HttpGet
        return name.EndsWith("Attribute", StringComparison.Ordinal) ? name : name + "Attribute";
    }

    // AttributeEntry is one attribute as the syntax spells it, which is all that is available when
    // the attribute's type does not resolve.
    private readonly record struct AttributeEntry(string Name, AttributeSyntax Syntax);

    private static List<AttributeEntry> SyntaxAttributes(SyntaxNode? node)
    {
        var out_ = new List<AttributeEntry>();
        var lists = node switch
        {
            MemberDeclarationSyntax m => m.AttributeLists,
            _ => default,
        };
        foreach (var list in lists)
        {
            foreach (var a in list.Attributes)
            {
                out_.Add(new AttributeEntry(NormalizeAttributeName(a.Name.ToString()), a));
            }
        }
        return out_;
    }

    // TemplateFromSyntax reads the route template out of an attribute's argument list. Only a
    // literal counts: a template built from a constant this scope cannot evaluate would produce a
    // path that matches nothing, which is worse than no route at all.
    private static string? TemplateFromSyntax(AttributeSyntax attr)
    {
        if (attr.ArgumentList == null) return null;
        foreach (var arg in attr.ArgumentList.Arguments)
        {
            if (arg.NameEquals != null && arg.NameEquals.Name.Identifier.Text is not ("Template" or "Route")) continue;
            if (arg.Expression is LiteralExpressionSyntax lit && lit.Token.Value is string s && s.Length > 0)
            {
                return s;
            }
        }
        return null;
    }

    private static IEnumerable<string> VerbsFromSyntax(AttributeSyntax attr)
    {
        if (attr.ArgumentList == null) yield break;
        foreach (var arg in attr.ArgumentList.Arguments)
        {
            if (arg.NameEquals != null) continue;
            if (arg.Expression is LiteralExpressionSyntax lit && lit.Token.Value is string s && s.Length > 0)
            {
                yield return s.ToUpperInvariant();
            }
        }
    }

    private static string NormalizeRoutePath(string path)
    {
        if (string.IsNullOrWhiteSpace(path)) return "";
        path = path.Trim();
        // Drop a query string and a scheme+host: neither participates in route matching.
        var q = path.IndexOf('?');
        if (q >= 0) path = path[..q];
        var scheme = path.IndexOf("://", StringComparison.Ordinal);
        if (scheme >= 0)
        {
            var slash = path.IndexOf('/', scheme + 3);
            path = slash >= 0 ? path[slash..] : "/";
        }
        var segments = path.Split('/', StringSplitOptions.RemoveEmptyEntries)
            .Select(NormalizeRouteSegment)
            .ToList();
        // Lower-cased because ASP.NET routing is case-insensitive: a client calling
        // /api/basketapi reaches a route declared [Route("api/[controller]")] on BasketApiController,
        // and comparing the two as written never matches. The Go side's routeMatchKey is an exact
        // string compare, so the case has to be settled here, on both emitters.
        return "/" + string.Join("/", segments).ToLowerInvariant();
    }

    private static string NormalizeRouteSegment(string seg)
    {
        // `{id}`, `{id:int}`, `{*catchAll}`, `{id?}` — all of them are "any value here".
        if (seg.StartsWith("{", StringComparison.Ordinal) && seg.EndsWith("}", StringComparison.Ordinal)) return "*";
        // An interpolation hole from a client call: $"api/orders/{id}" arrives with the braces.
        if (seg.Contains('{') && seg.Contains('}')) return "*";
        return seg;
    }

    private static void ExtractAspNetRoutes(MethodDeclarationSyntax md, string typeFq, SemanticModel model,
        List<SymbolDto> symbols, List<EdgeDto> edges)
    {
        var ms = model.GetDeclaredSymbol(md);
        if (ms == null) return;
        var typeDecl = md.Ancestors().OfType<TypeDeclarationSyntax>().FirstOrDefault();
        var tsym = typeDecl != null ? model.GetDeclaredSymbol(typeDecl) as INamedTypeSymbol : null;
        if (!IsRoutableController(tsym, typeDecl)) return;

        var classTemplate = typeDecl != null ? RouteTemplateFromSyntax(typeDecl) : null;
        var methodAttrs = SyntaxAttributes(md);

        // Verb attributes first; a [Route] with no verb beside it is a GET.
        var emitted = new HashSet<string>(StringComparer.Ordinal);
        var sawVerb = false;
        foreach (var entry in methodAttrs)
        {
            if (AspNetVerbAttributes.TryGetValue(entry.Name, out var http))
            {
                sawVerb = true;
                EmitRoute(http, TemplateFromSyntax(entry.Syntax) ?? "");
                continue;
            }
            if (entry.Name == "AcceptVerbsAttribute")
            {
                sawVerb = true;
                var template = TemplateFromSyntax(entry.Syntax) ?? "";
                foreach (var verb in VerbsFromSyntax(entry.Syntax))
                {
                    if (verb == template.ToUpperInvariant()) continue;
                    EmitRoute(verb, template);
                }
            }
        }
        if (!sawVerb)
        {
            foreach (var entry in methodAttrs)
            {
                if (entry.Name != "RouteAttribute") continue;
                EmitRoute("GET", TemplateFromSyntax(entry.Syntax) ?? "");
            }
        }

        void EmitRoute(string http, string tmpl)
        {
            var combined = CombineRoute(
                ExpandRouteTokens(classTemplate ?? "", tsym, ms),
                ExpandRouteTokens(tmpl, tsym, ms));
            var path = NormalizeRoutePath(combined);
            if (string.IsNullOrEmpty(path) || path == "/") return;
            var handlerFq = MethodFqName(ms);
            var routeFq = $"API_ROUTE:{http}:{path}@{handlerFq}";
            if (!emitted.Add(routeFq)) return;
            var (sl, el, sc, ec) = LineSpan(md);
            symbols.Add(new SymbolDto
            {
                Kind = "API_ROUTE",
                FqName = routeFq,
                StartLine = sl,
                EndLine = el,
                StartColumn = sc,
                EndColumn = ec,
                Signature = JsonSerializer.SerializeToElement(new
                {
                    http_method = http,
                    path_pattern = path,
                    handler_fq = handlerFq,
                    framework = "aspnet_core",
                }),
            });
            edges.Add(new EdgeDto { CallerFqName = routeFq, CalleeFqName = handlerFq, EdgeType = "ROUTE_TO_HANDLER" });
        }
    }

    private static IEnumerable<string> VerbStrings(TypedConstant arg)
    {
        if (arg.Kind == TypedConstantKind.Array)
        {
            foreach (var v in arg.Values)
            {
                if (v.Value is string s && s.Length > 0) yield return s.ToUpperInvariant();
            }
            yield break;
        }
        if (arg.Value is string one && one.Length > 0) yield return one.ToUpperInvariant();
    }

    private static string? GetNamedString(AttributeData attr, string name)
    {
        foreach (var na in attr.NamedArguments)
        {
            if (na.Key == name && na.Value.Value is string s && s.Length > 0) return s;
        }
        return null;
    }

    private static INamedTypeSymbol? tsymForRoutes(TypeDeclarationSyntax decl, SemanticModel model)
    {
        return model.GetDeclaredSymbol(decl) as INamedTypeSymbol;
    }

    // IsViewController distinguishes an MVC controller that renders a PAGE from an API controller
    // that returns data. The distinction decides which kind of test can drive it: a Razor view is a
    // browser surface, a JSON endpoint is not.
    //
    // [ApiController] is the explicit marker for the data kind. Otherwise a type deriving from
    // Controller (which adds view support) rather than ControllerBase renders views.
    private static bool IsViewController(INamedTypeSymbol? t, TypeDeclarationSyntax? decl = null)
    {
        if (t == null) return false;
        foreach (var entry in SyntaxAttributes(decl))
        {
            if (entry.Name == "ApiControllerAttribute") return false;
        }
        for (var cur = t.BaseType; cur != null; cur = cur.BaseType)
        {
            if (cur.Name == "Controller") return true;
            if (cur.Name == "ControllerBase") return false;
        }
        return false;
    }

    // ExtractMvcViewRoutes emits a PAGE_ROUTE for an action that renders a view.
    //
    // The path comes from ASP.NET's default route template, /{controller}/{action}, unless the type
    // or the method carries an explicit [Route]. That is the URL a browser test navigates to, and
    // nothing else in the index says what it is.
    private static void ExtractMvcViewRoutes(MethodDeclarationSyntax md, string typeFq, SemanticModel model,
        List<SymbolDto> symbols, List<EdgeDto> edges)
    {
        var ms = model.GetDeclaredSymbol(md);
        if (ms == null || ms.DeclaredAccessibility != Accessibility.Public) return;
        var typeDecl = md.Ancestors().OfType<TypeDeclarationSyntax>().FirstOrDefault();
        var tsym = typeDecl != null ? model.GetDeclaredSymbol(typeDecl) as INamedTypeSymbol : null;
        if (!IsViewController(tsym, typeDecl)) return;
        if (!ReturnsView(ms, md)) return;

        var explicitTemplate = RouteTemplateFromSyntax(md) ?? (typeDecl != null ? RouteTemplateFromSyntax(typeDecl) : null);
        string path;
        if (!string.IsNullOrEmpty(explicitTemplate))
        {
            path = NormalizeRoutePath(ExpandRouteTokens(explicitTemplate!, tsym, ms));
        }
        else
        {
            var controller = tsym!.Name;
            if (controller.EndsWith("Controller", StringComparison.Ordinal))
            {
                controller = controller[..^"Controller".Length];
            }
            // The default route makes Index the controller's root.
            path = NormalizeRoutePath("/" + controller + (ms.Name == "Index" ? "" : "/" + ms.Name));
        }
        if (string.IsNullOrEmpty(path) || path == "/") return;
        EmitPageRoute(path, MethodFqName(ms), "aspnet_mvc", md, symbols, edges);
    }

    // ReturnsView is true when the action's return type can carry a view. A method returning a
    // concrete DTO renders no page however the controller is declared.
    private static bool ReturnsView(IMethodSymbol ms, MethodDeclarationSyntax md)
    {
        var ret = ms.ReturnType;
        // Unwrap Task<T> / ValueTask<T>.
        if (ret is INamedTypeSymbol named && named.TypeArguments.Length == 1
            && (named.Name == "Task" || named.Name == "ValueTask"))
        {
            ret = named.TypeArguments[0];
        }
        if (ret.Name is "IActionResult" or "ActionResult" or "ViewResult" or "IResult") return true;
        // An explicit View(...) call is the other proof, for an action declared to return something
        // wider than a view result.
        return md.DescendantNodes().OfType<InvocationExpressionSyntax>().Any(inv =>
            inv.Expression is IdentifierNameSyntax id && id.Identifier.Text == "View");
    }

    // RazorPageHandlerPrefixes are the method names ASP.NET binds to HTTP verbs on a PageModel.
    private static readonly string[] RazorPageHandlerPrefixes = { "OnGet", "OnPost", "OnPut", "OnDelete", "OnPatch", "OnHead" };

    // ExtractRazorPageRoutes emits PAGE_ROUTEs for a Razor Pages model.
    //
    // The route comes from the FILE PATH, not from any attribute: Pages/Orders/Index.cshtml.cs
    // serves /Orders and /Orders/Index, and Pages/Orders/Detail.cshtml.cs serves /Orders/Detail.
    // Both spellings of an Index page are emitted because both are real URLs a test may navigate to.
    //
    // The `@page` directive in the .cshtml can override this; that file is the Go enricher's half
    // (CS12b), and the two dedupe on the normalised path.
    private static void ExtractRazorPageRoutes(TypeDeclarationSyntax decl, INamedTypeSymbol? tsym, string relPath,
        string typeFq, SemanticModel model, List<SymbolDto> symbols, List<EdgeDto> edges)
    {
        if (tsym == null) return;
        var isPageModel = false;
        for (var cur = tsym.BaseType; cur != null; cur = cur.BaseType)
        {
            if (cur.Name == "PageModel") { isPageModel = true; break; }
        }
        if (!isPageModel) return;

        var route = RazorPageRouteFromPath(relPath);
        if (route.Count == 0) return;

        foreach (var m in decl.Members.OfType<MethodDeclarationSyntax>())
        {
            var ms = model.GetDeclaredSymbol(m);
            if (ms == null || ms.DeclaredAccessibility != Accessibility.Public) continue;
            if (!RazorPageHandlerPrefixes.Any(pfx => ms.Name.StartsWith(pfx, StringComparison.Ordinal))) continue;
            foreach (var path in route)
            {
                EmitPageRoute(path, MethodFqName(ms), "razor-pages", m, symbols, edges);
            }
        }
    }

    // RazorPageRouteFromPath maps a code-behind path to the URLs it serves. Returns nothing for a
    // file outside a Pages/ root, which is where ASP.NET requires them.
    private static List<string> RazorPageRouteFromPath(string relPath)
    {
        var norm = relPath.Replace('\\', '/');
        var idx = norm.LastIndexOf("/Pages/", StringComparison.OrdinalIgnoreCase);
        string under;
        if (idx >= 0)
        {
            under = norm[(idx + "/Pages/".Length)..];
        }
        else if (norm.StartsWith("Pages/", StringComparison.OrdinalIgnoreCase))
        {
            under = norm["Pages/".Length..];
        }
        else
        {
            return new List<string>();
        }
        // Strip the code-behind suffix: Index.cshtml.cs -> Index.
        if (under.EndsWith(".cshtml.cs", StringComparison.OrdinalIgnoreCase))
        {
            under = under[..^".cshtml.cs".Length];
        }
        else if (under.EndsWith(".cs", StringComparison.OrdinalIgnoreCase))
        {
            under = under[..^".cs".Length];
        }
        under = under.Trim('/');
        if (under.Length == 0) return new List<string>();

        var full = NormalizeRoutePath("/" + under);
        var routes = new List<string> { full };
        // An Index page is also served at its directory.
        if (under.EndsWith("Index", StringComparison.OrdinalIgnoreCase))
        {
            var dir = under[..^"Index".Length].Trim('/');
            var dirRoute = dir.Length == 0 ? "/" : NormalizeRoutePath("/" + dir);
            if (dirRoute != full) routes.Add(dirRoute);
        }
        return routes;
    }

    private static void EmitPageRoute(string path, string handlerFq, string framework, SyntaxNode node,
        List<SymbolDto> symbols, List<EdgeDto> edges)
    {
        var fq = "PAGE_ROUTE:" + path + "@" + handlerFq;
        var (sl, el, sc, ec) = LineSpan(node);
        symbols.Add(new SymbolDto
        {
            Kind = "PAGE_ROUTE",
            FqName = fq,
            StartLine = sl,
            EndLine = el,
            StartColumn = sc,
            EndColumn = ec,
            Signature = JsonSerializer.SerializeToElement(new
            {
                path_pattern = path,
                handler_fq = handlerFq,
                framework,
            }),
        });
        edges.Add(new EdgeDto { CallerFqName = fq, CalleeFqName = handlerFq, EdgeType = "ROUTE_TO_HANDLER" });
    }

    private static string? RouteTemplateFromSyntax(SyntaxNode node)
    {
        foreach (var entry in SyntaxAttributes(node))
        {
            if (entry.Name != "RouteAttribute") continue;
            var t = TemplateFromSyntax(entry.Syntax);
            if (!string.IsNullOrEmpty(t)) return t;
        }
        return null;
    }

    private static string? GetRouteTemplate(ImmutableArray<AttributeData> attrs)
    {
        foreach (var a in attrs)
        {
            if (a.AttributeClass?.Name == "RouteAttribute")
                return GetTemplateFromAttribute(a);
        }
        return null;
    }

    private static string? GetTemplateFromAttribute(AttributeData attr)
    {
        foreach (var arg in attr.ConstructorArguments)
        {
            if (arg.Value is string s && !string.IsNullOrEmpty(s)) return s;
        }
        foreach (var na in attr.NamedArguments)
        {
            if (na.Key == "Template" && na.Value.Value is string s2 && !string.IsNullOrEmpty(s2)) return s2;
        }
        return null;
    }

    private static string CombineRoute(string? classTemplate, string methodTemplate)
    {
        classTemplate = (classTemplate ?? "").Trim().Trim('/');
        methodTemplate = (methodTemplate ?? "").Trim().Trim('/');
        if (classTemplate == "" && methodTemplate == "") return "";
        if (classTemplate == "") return "/" + methodTemplate;
        if (methodTemplate == "") return "/" + classTemplate;
        return "/" + classTemplate + "/" + methodTemplate;
    }

    // DotnetHttpVerbs are the HttpClient calls that name a verb, including the System.Net.Http.Json
    // extension methods. Five of these ten were recognised, so a test written with the JSON helpers
    // — which is how modern .NET calls an API — produced no client request and therefore no
    // TARGETS_API_ROUTE edge, leaving the route it exercised reported as uncovered.
    private static readonly Dictionary<string, string> DotnetHttpVerbs = new(StringComparer.Ordinal)
    {
        ["GetAsync"] = "GET",
        ["PostAsync"] = "POST",
        ["PutAsync"] = "PUT",
        ["DeleteAsync"] = "DELETE",
        ["PatchAsync"] = "PATCH",
        ["GetStringAsync"] = "GET",
        ["GetStreamAsync"] = "GET",
        ["GetByteArrayAsync"] = "GET",
        ["GetFromJsonAsync"] = "GET",
        ["PostAsJsonAsync"] = "POST",
        ["PutAsJsonAsync"] = "PUT",
        ["PatchAsJsonAsync"] = "PATCH",
        ["DeleteFromJsonAsync"] = "DELETE",
        // SendAsync carries its verb on the request message; GET is the honest default when the
        // message is not a literal this scope can read.
        ["SendAsync"] = "GET",
    };

    // DistinctiveHttpMethodNames belong to System.Net.Http and to nothing else a repository is
    // likely to declare, so seeing one is proof enough on its own.
    private static readonly HashSet<string> DistinctiveHttpMethodNames = new(StringComparer.Ordinal)
    {
        "GetFromJsonAsync", "PostAsJsonAsync", "PutAsJsonAsync", "PatchAsJsonAsync",
        "DeleteFromJsonAsync", "GetStringAsync", "GetByteArrayAsync", "GetStreamAsync",
    };

    // IsHttpClientCall decides whether an invocation is an outbound HTTP request.
    //
    // The symbol is the reliable answer and is preferred, but it is frequently absent: the
    // compilations this tool builds reference the BCL and nothing else, so the System.Net.Http.Json
    // extension methods — which is how a modern .NET test calls an API — do not bind at all. Since
    // a name like GetFromJsonAsync belongs to that namespace and to nothing else a repository
    // declares, the syntax alone is proof for those. The ambiguous names (GetAsync, SendAsync, and
    // the rest, which any repository method could be called) still need either the symbol or a
    // receiver the source itself declares as an HttpClient.
    private static bool IsHttpClientCall(string name, InvocationExpressionSyntax inv,
        MemberAccessExpressionSyntax ma, SemanticModel model)
    {
        if (model.GetSymbolInfo(inv).Symbol is IMethodSymbol sym)
        {
            var containing = sym.ContainingType.ToDisplayString(SymbolDisplayFormat.FullyQualifiedFormat);
            if (containing.Contains("System.Net.Http", StringComparison.Ordinal)) return true;
            var receiver = model.GetTypeInfo(ma.Expression).Type?.ToDisplayString(SymbolDisplayFormat.FullyQualifiedFormat) ?? "";
            if (receiver.Contains("System.Net.Http.HttpClient", StringComparison.Ordinal)) return true;
        }
        if (DistinctiveHttpMethodNames.Contains(name)) return true;
        // An ambiguous name needs the source to say its receiver is an HttpClient.
        var root = inv.SyntaxTree.GetRoot();
        if (ma.Expression is IdentifierNameSyntax id)
        {
            var ident = id.Identifier.Text;
            return HttpClientDeclarationRE.IsMatch(root.ToString()) && root.ToString().Contains(ident, StringComparison.Ordinal)
                && System.Text.RegularExpressions.Regex.IsMatch(root.ToString(),
                    @"\bHttpClient\b[^;=\n]*\b" + System.Text.RegularExpressions.Regex.Escape(ident) + @"\b");
        }
        return false;
    }

    private static readonly System.Text.RegularExpressions.Regex HttpClientDeclarationRE =
        new(@"\bHttpClient\b", System.Text.RegularExpressions.RegexOptions.Compiled);

    // SelectorMethodNames are the Playwright .NET calls that address an element by a literal.
    //
    // What a test SELECTS is as much a fact about the UI as what the markup declares, and it is the
    // better one: it is the selector somebody already proved works. Without this the selector
    // inventory a generated test reads is built from markup alone, so a convention established in
    // the existing tests — a page-object helper, a chosen data-testid scheme — is invisible.
    private static readonly HashSet<string> SelectorMethodNames = new(StringComparer.Ordinal)
    {
        "GetByTestId", "Locator", "QuerySelectorAsync", "QuerySelectorAllAsync",
        "GetByRole", "GetByLabel", "GetByPlaceholder", "GetByText", "GetByTitle", "GetByAltText",
    };

    // CollectTestSelectors emits TEST_SELECTOR symbols for the selectors a test file uses, linked to
    // the file's E2E_SPEC so retrieval can reach them from the spec.
    private static void CollectTestSelectors(SyntaxNode root, string relPath, bool isTest,
        List<SymbolDto> symbols, List<EdgeDto> edges)
    {
        if (!isTest) return;
        var specFq = "E2E_SPEC:" + relPath;
        var seen = new HashSet<string>(StringComparer.Ordinal);
        foreach (var inv in root.DescendantNodes().OfType<InvocationExpressionSyntax>())
        {
            var name = inv.Expression switch
            {
                MemberAccessExpressionSyntax ma => ma.Name.Identifier.Text,
                IdentifierNameSyntax id => id.Identifier.Text,
                _ => null,
            };
            if (name == null || !SelectorMethodNames.Contains(name)) continue;
            if (inv.ArgumentList.Arguments.Count == 0) continue;
            // Only a literal counts. A selector built from a variable is a string this scope cannot
            // read, and recording the expression text would put source code where a selector goes.
            if (inv.ArgumentList.Arguments[0].Expression is not LiteralExpressionSyntax lit) continue;
            if (lit.Token.Value is not string value || value.Length == 0) continue;

            var fq = $"TEST_SELECTOR:{relPath}#{name}={value}";
            if (!seen.Add(fq)) continue;
            var (sl, el, sc, ec) = LineSpan(inv);
            symbols.Add(new SymbolDto
            {
                Kind = "TEST_SELECTOR",
                FqName = fq,
                StartLine = sl,
                EndLine = el,
                StartColumn = sc,
                EndColumn = ec,
                Signature = JsonSerializer.SerializeToElement(new Dictionary<string, string>
                {
                    ["method"] = name,
                    ["selector"] = value,
                }),
            });
            edges.Add(new EdgeDto { CallerFqName = specFq, CalleeFqName = fq, EdgeType = "USES_SELECTOR" });
        }

        // Selenium page objects declare their selectors as attributes rather than calls.
        foreach (var field in root.DescendantNodes().OfType<FieldDeclarationSyntax>())
        {
            foreach (var entry in SyntaxAttributes(field))
            {
                if (entry.Name != "FindsByAttribute") continue;
                var using_ = GetNamedAttributeArgument(entry.Syntax, "Using");
                if (string.IsNullOrEmpty(using_)) continue;
                var how = GetNamedAttributeArgument(entry.Syntax, "How") ?? "How.Id";
                var fq = $"TEST_SELECTOR:{relPath}#{how}={using_}";
                if (!seen.Add(fq)) continue;
                var (sl, el, sc, ec) = LineSpan(field);
                symbols.Add(new SymbolDto
                {
                    Kind = "TEST_SELECTOR",
                    FqName = fq,
                    StartLine = sl,
                    EndLine = el,
                    StartColumn = sc,
                    EndColumn = ec,
                    Signature = JsonSerializer.SerializeToElement(new Dictionary<string, string>
                    {
                        ["method"] = how!,
                        ["selector"] = using_!,
                    }),
                });
                edges.Add(new EdgeDto { CallerFqName = specFq, CalleeFqName = fq, EdgeType = "USES_SELECTOR" });
            }
        }
    }

    // GetNamedAttributeArgument reads `Name = value` out of an attribute's argument list. A literal
    // yields its text; anything else yields the expression as written, which is what `How.Id` is.
    private static string? GetNamedAttributeArgument(AttributeSyntax attr, string name)
    {
        if (attr.ArgumentList == null) return null;
        foreach (var arg in attr.ArgumentList.Arguments)
        {
            if (arg.NameEquals?.Name.Identifier.Text != name) continue;
            if (arg.Expression is LiteralExpressionSyntax lit && lit.Token.Value is string s) return s;
            return arg.Expression.ToString();
        }
        return null;
    }

    private static void CollectHttpClientRequests(SyntaxNode root, SemanticModel model, List<SymbolDto> symbols, List<EdgeDto> edges)
    {
        foreach (var inv in root.DescendantNodes().OfType<InvocationExpressionSyntax>())
        {
            if (inv.Expression is not MemberAccessExpressionSyntax ma) continue;
            var name = ma.Name.Identifier.Text;
            if (!DotnetHttpVerbs.TryGetValue(name, out var method)) continue;
            if (!IsHttpClientCall(name, inv, ma, model)) continue;

            string? path = null;
            if (inv.ArgumentList.Arguments.Count > 0)
            {
                var arg0 = inv.ArgumentList.Arguments[0].Expression;
                path = TryGetStringConstant(model, arg0) ?? InterpolatedRoutePath(arg0);
            }

            if (string.IsNullOrEmpty(path)) continue;
            // Both sides of a route match normalise the same way, or a parameterised route can
            // never be matched: routeMatchKey on the Go side is an exact string compare.
            path = NormalizeRoutePath(path);
            if (string.IsNullOrEmpty(path) || path == "/") continue;

            var callerFq = FindEnclosingCallableFq(inv, model);
            if (string.IsNullOrEmpty(callerFq)) continue;

            var line = inv.GetLocation().GetLineSpan().StartLinePosition.Line + 1;
            var endLine = inv.GetLocation().GetLineSpan().EndLinePosition.Line + 1;
            var symFq = $"API_CLIENT_REQUEST:{method}:{path}@{callerFq}:L{line}";
            symbols.Add(new SymbolDto
            {
                Kind = "API_CLIENT_REQUEST",
                FqName = symFq,
                StartLine = line,
                EndLine = endLine,
                Signature = JsonSerializer.SerializeToElement(new Dictionary<string, string>
                {
                    ["framework"] = "dotnet_http",
                    ["http_method"] = method,
                    ["path_pattern"] = path,
                }),
            });
            edges.Add(new EdgeDto { CallerFqName = callerFq, CalleeFqName = symFq, EdgeType = "CALLS_API" });
        }
    }

    // InterpolatedRoutePath renders $"api/orders/{id}" as "api/orders/{}" so the normaliser can turn
    // the hole into the same wildcard a route template's {id} becomes.
    //
    // An interpolated path is the NORMAL way a test addresses a parameterised endpoint, and it is
    // not a constant — so requiring a constant dropped exactly the calls that exercise the routes
    // most worth knowing about. A hole is "some value", which is all the match needs.
    private static string? InterpolatedRoutePath(ExpressionSyntax expr)
    {
        if (expr is not InterpolatedStringExpressionSyntax interp) return null;
        var sb = new System.Text.StringBuilder();
        foreach (var content in interp.Contents)
        {
            switch (content)
            {
                case InterpolatedStringTextSyntax text:
                    sb.Append(text.TextToken.ValueText);
                    break;
                case InterpolationSyntax:
                    sb.Append("{}");
                    break;
            }
        }
        var outp = sb.ToString();
        return outp.Length > 0 ? outp : null;
    }

    private static string? TryGetStringConstant(SemanticModel model, ExpressionSyntax expr)
    {
        var c = model.GetConstantValue(expr);
        if (c.HasValue && c.Value is string s) return s;
        if (expr is LiteralExpressionSyntax lit && lit.Token.Value is string s2) return s2;
        return null;
    }

    // FQName format (Spec 4(b), B25 — BREAKING, requires reindex):
    //   types:   Namespace.Type<T,U>          (declared type parameters, via OriginalDefinition)
    //   methods: Namespace.Type<T>#M<TM>(int,List<T>)   — parameter list ALWAYS present, "()" when empty
    //   ctors:   Namespace.Type#.ctor(string)
    //   fields/properties/events: Namespace.Type<T>#Name (not callable — no parameter list)
    //
    // Why OriginalDefinition everywhere: a USE site sees constructed symbols (IRepository<Order>,
    // Add(int) on List<int>), and rendering those would never equal the DECLARATION's FQName, so
    // every edge into a generic type would dangle. The definition form makes both sides render
    // identically. Parameter types render name-only with language keywords (int, string) — no
    // namespaces, so the '#'-suffix stays free of dots and every legacy "last [.#] wins" consumer
    // keeps a clean split point after BareFQName stripping.
    private static string MethodFqName(IMethodSymbol m)
    {
        m = m.OriginalDefinition;
        var typeFq = TypeFqName(m.ContainingType);
        var name = m.MethodKind == MethodKind.Constructor ? ".ctor" : m.Name;
        if (m.TypeParameters.Length > 0)
        {
            name += "<" + string.Join(",", m.TypeParameters.Select(tp => tp.Name)) + ">";
        }
        return typeFq + "#" + name + "(" + string.Join(",", m.Parameters.Select(pr => ParamTypeName(pr.Type))) + ")";
    }

    private static string ParamTypeName(ITypeSymbol t)
    {
        var fmt = new SymbolDisplayFormat(
            globalNamespaceStyle: SymbolDisplayGlobalNamespaceStyle.Omitted,
            typeQualificationStyle: SymbolDisplayTypeQualificationStyle.NameOnly,
            genericsOptions: SymbolDisplayGenericsOptions.IncludeTypeParameters,
            miscellaneousOptions: SymbolDisplayMiscellaneousOptions.EscapeKeywordIdentifiers |
                                  SymbolDisplayMiscellaneousOptions.UseSpecialTypes);
        return t.ToDisplayString(fmt);
    }

    private static string TypeFqName(INamedTypeSymbol t)
    {
        t = (INamedTypeSymbol)t.OriginalDefinition;
        var fmt = new SymbolDisplayFormat(
            globalNamespaceStyle: SymbolDisplayGlobalNamespaceStyle.Omitted,
            typeQualificationStyle: SymbolDisplayTypeQualificationStyle.NameAndContainingTypesAndNamespaces,
            genericsOptions: SymbolDisplayGenericsOptions.IncludeTypeParameters,
            miscellaneousOptions: SymbolDisplayMiscellaneousOptions.EscapeKeywordIdentifiers);
        return t.ToDisplayString(fmt);
    }

    // BareFqName is the pre-B25 form — generic markers and parameter lists stripped — stored in
    // signature_json.bare_fq_name so name-only lookups (a model calling get_symbol with what it
    // read in prose) still resolve.
    private static string BareFqName(string fq)
    {
        var hash = fq.IndexOf('#');
        var typePart = hash >= 0 ? fq[..hash] : fq;
        var memberPart = hash >= 0 ? fq[(hash + 1)..] : "";
        var paren = memberPart.IndexOf('(');
        if (paren >= 0) memberPart = memberPart[..paren];
        typePart = StripAngles(typePart);
        memberPart = StripAngles(memberPart);
        return hash >= 0 ? typePart + "#" + memberPart : typePart;
    }

    private static string StripAngles(string s)
    {
        if (!s.Contains('<')) return s;
        var b = new System.Text.StringBuilder(s.Length);
        var depth = 0;
        foreach (var c in s)
        {
            if (c == '<') { depth++; continue; }
            if (c == '>') { if (depth > 0) depth--; continue; }
            if (depth == 0) b.Append(c);
        }
        return b.ToString();
    }

    // The signature payloads below carry the keys the Java indexer emits, because every consumer
    // downstream was written against those: retrieval's context builder, the testability scorer and
    // the generator's per-symbol API surface all read `signature`, `params` and `return_type`. C#
    // emitted none of them, so a C# symbol reached the prompt as a name and a line range — and
    // apisurface.SignatureTargets, which resolves the third-party types in a signature, had nothing
    // to read and returned nil for every C# gap.

    // TopLevelModuleName names the container for a file that declares no namespace.
    //
    // The project's RootNamespace would be the truthful answer, but this scope has no csproj — so
    // the fallback is the assembly-neutral "Global", which is what C# itself calls the namespace a
    // top-level declaration lands in. Anything is better than "": an empty module drops every
    // CONTAINS edge and leaves the file's symbols with no container to chunk by.
    private static string TopLevelModuleName(SyntaxNode root)
    {
        var hasContent = root.DescendantNodes().OfType<GlobalStatementSyntax>().Any()
            || root.DescendantNodes().OfType<BaseTypeDeclarationSyntax>().Any()
            || root.DescendantNodes().OfType<DelegateDeclarationSyntax>().Any();
        return hasContent ? "Global" : "";
    }

    // IndexEnum emits the enum and its members. The member NAMES are the point: a test that
    // switches over a state machine needs to know the states exist, and they are the one kind of
    // member that carries no modifier for a scan to key on.
    private static void IndexEnum(EnumDeclarationSyntax ed, string moduleNs, SemanticModel model,
        List<SymbolDto> symbols, Action<string, string, string> addEdge)
    {
        var sym = model.GetDeclaredSymbol(ed);
        if (sym == null) return;
        var typeFq = TypeFqName(sym);
        var (sl, el, sc, ec) = LineSpan(ed);

        var members = ed.Members.Select(m => m.Identifier.Text).Where(n => n.Length > 0).ToList();
        var payload = new Dictionary<string, object>
        {
            ["visibility"] = sym.DeclaredAccessibility.ToString().ToLowerInvariant(),
            ["bare_fq_name"] = BareFqName(typeFq),
            ["exported"] = IsExported(sym),
            ["members"] = members,
            ["signature"] = CollapseWhitespace("enum " + ed.Identifier.Text
                + (ed.BaseList != null ? " " + ed.BaseList.ToString() : "")),
        };
        var attrs = AttributeSimpleNames(sym);
        if (attrs.Count > 0) payload["attributes"] = attrs;
        var doc = XmlDocSummary(sym);
        if (!string.IsNullOrEmpty(doc)) payload["xmldoc"] = doc;

        symbols.Add(new SymbolDto
        {
            Kind = "enum",
            FqName = typeFq,
            StartLine = sl,
            EndLine = el,
            StartColumn = sc,
            EndColumn = ec,
            Signature = JsonSerializer.SerializeToElement(payload),
        });
        if (!string.IsNullOrEmpty(moduleNs))
        {
            addEdge(moduleNs, typeFq, "CONTAINS");
        }
        // The underlying type is an implementation detail, so no EXTENDS edge: it says nothing
        // about how the enum is used and would rank as inheritance evidence in retrieval.
        foreach (var m in ed.Members)
        {
            var ms = model.GetDeclaredSymbol(m);
            if (ms == null) continue;
            var fq = typeFq + "#" + m.Identifier.Text;
            var (msl, mel, msc, mec) = LineSpan(m);
            symbols.Add(new SymbolDto
            {
                Kind = "field",
                FqName = fq,
                StartLine = msl,
                EndLine = mel,
                StartColumn = msc,
                EndColumn = mec,
                Signature = BuildMemberSignature(ms, isStatic: true),
            });
            addEdge(typeFq, fq, "CONTAINS");
        }
    }

    // IndexRecordPositionalProperties emits a record's parameter list as properties.
    //
    // `public record Order(int Id, string Sku)` declares Id and Sku as properties and nothing in
    // the body says so. A record indexed without them is a type with no members, so a gap against
    // one is proposed blind.
    private static void IndexRecordPositionalProperties(TypeDeclarationSyntax decl, string typeFq,
        SemanticModel model, List<SymbolDto> symbols, Action<string, string, string> addEdge)
    {
        if (decl is not RecordDeclarationSyntax rd || rd.ParameterList == null) return;
        foreach (var p in rd.ParameterList.Parameters)
        {
            var name = p.Identifier.Text;
            if (name.Length == 0) continue;
            var fq = typeFq + "#" + name;
            var (sl, el, sc, ec) = LineSpan(p);
            var type = p.Type != null ? model.GetTypeInfo(p.Type).Type : null;
            var payload = new Dictionary<string, object>
            {
                ["visibility"] = "public",
                ["static"] = false,
                ["exported"] = true,
                ["positional"] = true,
            };
            if (type != null) payload["type"] = DisplayTypeName(type);
            symbols.Add(new SymbolDto
            {
                Kind = "property",
                FqName = fq,
                StartLine = sl,
                EndLine = el,
                StartColumn = sc,
                EndColumn = ec,
                Signature = JsonSerializer.SerializeToElement(payload),
            });
            addEdge(typeFq, fq, "CONTAINS");
        }
    }

    // An indexer is a property whose name is `this[]`; there is no other way to spell it.
    private static void IndexIndexer(IndexerDeclarationSyntax ixd, string typeFq, SemanticModel model,
        List<SymbolDto> symbols, Action<string, string, string> addEdge)
    {
        var sym = model.GetDeclaredSymbol(ixd);
        if (sym == null) return;
        var fq = typeFq + "#this[" + string.Join(",", sym.Parameters.Select(pr => ParamTypeName(pr.Type))) + "]";
        var (sl, el, sc, ec) = LineSpan(ixd);
        symbols.Add(new SymbolDto
        {
            Kind = "property",
            FqName = fq,
            StartLine = sl,
            EndLine = el,
            StartColumn = sc,
            EndColumn = ec,
            Signature = BuildMemberSignature(sym, sym.IsStatic),
        });
        addEdge(typeFq, fq, "CONTAINS");
    }

    // An operator is a method with a symbolic name. A test asserting `a + b` calls one, and without
    // this the call resolves to nothing.
    private static void IndexOperator(OperatorDeclarationSyntax opd, string typeFq, SemanticModel model,
        List<SymbolDto> symbols, Action<string, string, string> addEdge)
    {
        var sym = model.GetDeclaredSymbol(opd);
        if (sym == null) return;
        EmitCallableSymbol(sym, opd, typeFq, symbols, addEdge);
    }

    private static void IndexConversionOperator(ConversionOperatorDeclarationSyntax cod, string typeFq,
        SemanticModel model, List<SymbolDto> symbols, Action<string, string, string> addEdge)
    {
        var sym = model.GetDeclaredSymbol(cod);
        if (sym == null) return;
        EmitCallableSymbol(sym, cod, typeFq, symbols, addEdge);
    }

    private static void EmitCallableSymbol(IMethodSymbol sym, BaseMethodDeclarationSyntax decl, string typeFq,
        List<SymbolDto> symbols, Action<string, string, string> addEdge)
    {
        var fq = MethodFqName(sym);
        var (sl, el, sc, ec) = LineSpan(decl);
        symbols.Add(new SymbolDto
        {
            Kind = "method",
            FqName = fq,
            StartLine = sl,
            EndLine = el,
            StartColumn = sc,
            EndColumn = ec,
            Signature = BuildMethodSignature(sym, decl),
        });
        addEdge(typeFq, fq, "CONTAINS");
    }

    // A delegate is a type, not a member, but it is declared like one and a callback parameter's
    // shape is exactly what a test has to construct.
    private static void IndexNestedDelegate(DelegateDeclarationSyntax dd, string containerFq, SemanticModel model,
        List<SymbolDto> symbols, Action<string, string, string> addEdge)
    {
        var sym = model.GetDeclaredSymbol(dd);
        if (sym == null) return;
        var fq = TypeFqName(sym);
        var (sl, el, sc, ec) = LineSpan(dd);
        var payload = new Dictionary<string, object>
        {
            ["visibility"] = sym.DeclaredAccessibility.ToString().ToLowerInvariant(),
            ["bare_fq_name"] = BareFqName(fq),
            ["exported"] = IsExported(sym),
            ["signature"] = CollapseWhitespace(dd.ToString().TrimEnd(';')),
        };
        if (sym.DelegateInvokeMethod != null)
        {
            payload["return_type"] = DisplayTypeName(sym.DelegateInvokeMethod.ReturnType);
            payload["params"] = sym.DelegateInvokeMethod.Parameters.Select(p => new Dictionary<string, object>
            {
                ["name"] = p.Name,
                ["type"] = DisplayTypeName(p.Type),
            }).ToList();
        }
        var doc = XmlDocSummary(sym);
        if (!string.IsNullOrEmpty(doc)) payload["xmldoc"] = doc;

        symbols.Add(new SymbolDto
        {
            Kind = "delegate",
            FqName = fq,
            StartLine = sl,
            EndLine = el,
            StartColumn = sc,
            EndColumn = ec,
            Signature = JsonSerializer.SerializeToElement(payload),
        });
        if (!string.IsNullOrEmpty(containerFq))
        {
            addEdge(containerFq, fq, "CONTAINS");
        }
    }

    private static JsonElement? BuildTypeSignature(INamedTypeSymbol t, TypeDeclarationSyntax? decl = null)
    {
        var payload = new Dictionary<string, object>
        {
            ["visibility"] = t.DeclaredAccessibility.ToString().ToLowerInvariant(),
            ["bare_fq_name"] = BareFqName(TypeFqName(t)),
            ["exported"] = IsExported(t),
        };
        if (decl != null)
        {
            payload["signature"] = DeclarationHeaderText(decl);
        }
        var attrs = AttributeSimpleNames(t);
        if (attrs.Count > 0)
        {
            payload["attributes"] = attrs;
        }
        var doc = XmlDocSummary(t);
        if (!string.IsNullOrEmpty(doc))
        {
            payload["xmldoc"] = doc;
        }
        return JsonSerializer.SerializeToElement(payload);
    }

    private static JsonElement? BuildMethodSignature(IMethodSymbol m, BaseMethodDeclarationSyntax? decl = null)
    {
        var payload = new Dictionary<string, object>
        {
            ["visibility"] = m.DeclaredAccessibility.ToString().ToLowerInvariant(),
            ["static"] = m.IsStatic,
            ["bare_fq_name"] = BareFqName(MethodFqName(m)),
            ["exported"] = IsExported(m),
            ["params"] = m.Parameters.Select(p => new Dictionary<string, object>
            {
                ["name"] = p.Name,
                ["type"] = DisplayTypeName(p.Type),
            }).ToList(),
        };
        if (m.MethodKind != MethodKind.Constructor)
        {
            payload["return_type"] = DisplayTypeName(m.ReturnType);
        }
        payload["signature"] = decl != null ? DeclarationHeaderText(decl) : SynthesiseCallableSignature(m);
        var attrs = AttributeSimpleNames(m);
        if (attrs.Count > 0)
        {
            payload["attributes"] = attrs;
        }
        var doc = XmlDocSummary(m);
        if (!string.IsNullOrEmpty(doc))
        {
            payload["xmldoc"] = doc;
        }
        return JsonSerializer.SerializeToElement(payload);
    }

    private static JsonElement? BuildMemberSignature(ISymbol s, bool isStatic)
    {
        var payload = new Dictionary<string, object>
        {
            ["visibility"] = s.DeclaredAccessibility.ToString().ToLowerInvariant(),
            ["static"] = isStatic,
            ["exported"] = IsExported(s),
        };
        var t = s switch
        {
            IFieldSymbol f => f.Type,
            IPropertySymbol pr => pr.Type,
            IEventSymbol e => e.Type,
            _ => null,
        };
        if (t != null)
        {
            payload["type"] = DisplayTypeName(t);
        }
        var attrs = AttributeSimpleNames(s);
        if (attrs.Count > 0)
        {
            payload["attributes"] = attrs;
        }
        var doc = XmlDocSummary(s);
        if (!string.IsNullOrEmpty(doc))
        {
            payload["xmldoc"] = doc;
        }
        return JsonSerializer.SerializeToElement(payload);
    }

    // IsExported is "visible outside its own assembly", the same question the Java indexer answers
    // with `public`. Nested accessibility matters: a public method on an internal type is not
    // reachable from a test assembly, and reporting it as exported would put it in front of a
    // generator that cannot call it.
    private static bool IsExported(ISymbol s)
    {
        for (var cur = s; cur != null; cur = cur.ContainingType)
        {
            switch (cur.DeclaredAccessibility)
            {
                case Accessibility.Public:
                case Accessibility.Protected:
                case Accessibility.ProtectedOrInternal:
                    continue;
                case Accessibility.NotApplicable:
                    continue;
                default:
                    return false;
            }
        }
        return true;
    }

    // DeclarationHeaderText is the declaration with its body removed — what a reader needs to call
    // the member and nothing more. Roslyn gives the whole node, so the body, the expression body
    // and the trailing semicolon are trimmed, and the result is collapsed onto one line.
    private static string DeclarationHeaderText(SyntaxNode decl)
    {
        var header = decl switch
        {
            MethodDeclarationSyntax m => m.WithBody(null).WithExpressionBody(null).WithSemicolonToken(default).ToString(),
            ConstructorDeclarationSyntax c => c.WithBody(null).WithExpressionBody(null).WithSemicolonToken(default).ToString(),
            OperatorDeclarationSyntax o => o.WithBody(null).WithExpressionBody(null).WithSemicolonToken(default).ToString(),
            ConversionOperatorDeclarationSyntax v => v.WithBody(null).WithExpressionBody(null).WithSemicolonToken(default).ToString(),
            TypeDeclarationSyntax t => TypeHeaderText(t),
            _ => decl.ToString(),
        };
        return CollapseWhitespace(header);
    }

    private static string TypeHeaderText(TypeDeclarationSyntax t)
    {
        // Everything up to the opening brace, or to the semicolon for a record with no body.
        var text = t.ToString();
        var brace = text.IndexOf('{');
        if (brace >= 0)
        {
            text = text[..brace];
        }
        return text.TrimEnd().TrimEnd(';');
    }

    // SynthesiseCallableSignature rebuilds a declaration for a callable whose syntax this scope does
    // not have — a primary constructor, or a record's generated members.
    private static string SynthesiseCallableSignature(IMethodSymbol m)
    {
        var ps = string.Join(", ", m.Parameters.Select(p => DisplayTypeName(p.Type) + " " + p.Name));
        var name = m.MethodKind == MethodKind.Constructor ? m.ContainingType.Name : m.Name;
        var ret = m.MethodKind == MethodKind.Constructor ? "" : DisplayTypeName(m.ReturnType) + " ";
        var vis = m.DeclaredAccessibility == Accessibility.Public ? "public " : "";
        return CollapseWhitespace(vis + ret + name + "(" + ps + ")");
    }

    private static string CollapseWhitespace(string s)
    {
        return string.Join(" ", s.Split((char[]?)null, StringSplitOptions.RemoveEmptyEntries)).Trim();
    }

    // DisplayTypeName renders a type the way C# source spells it — `string`, `int?`,
    // `List<Order>` — rather than the metadata form. This is read alongside source, and the
    // keyword form is what a compiler error will quote back.
    private static string DisplayTypeName(ITypeSymbol t)
    {
        var fmt = new SymbolDisplayFormat(
            globalNamespaceStyle: SymbolDisplayGlobalNamespaceStyle.Omitted,
            typeQualificationStyle: SymbolDisplayTypeQualificationStyle.NameAndContainingTypes,
            genericsOptions: SymbolDisplayGenericsOptions.IncludeTypeParameters,
            miscellaneousOptions: SymbolDisplayMiscellaneousOptions.UseSpecialTypes
                | SymbolDisplayMiscellaneousOptions.IncludeNullableReferenceTypeModifier);
        return t.ToDisplayString(fmt);
    }

    private static List<string> AttributeSimpleNames(ISymbol s)
    {
        var out_ = new List<string>();
        foreach (var a in s.GetAttributes())
        {
            var name = a.AttributeClass?.Name;
            if (string.IsNullOrEmpty(name)) continue;
            // `[Fact]` is FactAttribute; the source spelling is the one a reader recognises.
            if (name.EndsWith("Attribute", StringComparison.Ordinal) && name.Length > "Attribute".Length)
            {
                name = name[..^"Attribute".Length];
            }
            if (!out_.Contains(name)) out_.Add(name);
        }
        return out_;
    }

    private const int MaxXmlDocRunes = 512;

    // XmlDocSummary extracts the <summary> text from a symbol's documentation comment. The comment
    // is the author's own statement of intent, and it is the one piece of a C# file that says WHY
    // rather than what.
    private static string XmlDocSummary(ISymbol s)
    {
        var xml = s.GetDocumentationCommentXml();
        if (string.IsNullOrWhiteSpace(xml)) return "";
        var open = xml.IndexOf("<summary>", StringComparison.Ordinal);
        if (open < 0) return "";
        var close = xml.IndexOf("</summary>", open, StringComparison.Ordinal);
        if (close < 0) return "";
        var body = xml[(open + "<summary>".Length)..close];
        // Inner elements (<see cref="..."/>, <paramref .../>) are dropped to their text.
        body = System.Text.RegularExpressions.Regex.Replace(body, "<[^>]*>", " ");
        body = System.Net.WebUtility.HtmlDecode(body);
        body = CollapseWhitespace(body);
        return body.Length > MaxXmlDocRunes ? body[..MaxXmlDocRunes] : body;
    }

    private static string GuessModuleNamespace(SyntaxNode root)
    {
        var fs = root.DescendantNodes().OfType<FileScopedNamespaceDeclarationSyntax>().FirstOrDefault();
        if (fs != null) return fs.Name.ToString().Trim();
        var block = root.DescendantNodes().OfType<NamespaceDeclarationSyntax>().FirstOrDefault();
        if (block != null) return block.Name.ToString().Trim();
        return "";
    }

    // IsLikelyTestPath recognises the .NET test-project conventions.
    //
    // The previous rule required the file name to end in "Tests" with that exact casing and missed
    // the singular form entirely, so FooTest.cs — the MSTest and NUnit convention — was indexed as
    // production code. A test file misread that way is worse than an unindexed one: its symbols
    // become gap candidates, so the run proposes writing tests for the tests.
    // DetectE2EFramework names how a .NET test drives the application end to end, or null when it
    // does not. The three are not interchangeable: a browser test needs a running server and a URL,
    // while WebApplicationFactory starts the app in-process and needs neither.
    private static string? DetectE2EFramework(string text)
    {
        if (text.Contains("Microsoft.Playwright", StringComparison.Ordinal)) return "playwright-dotnet";
        if (text.Contains("OpenQA.Selenium", StringComparison.Ordinal)) return "selenium-dotnet";
        if (text.Contains("Microsoft.AspNetCore.Mvc.Testing", StringComparison.Ordinal)
            || text.Contains("WebApplicationFactory", StringComparison.Ordinal))
        {
            return "webapplicationfactory";
        }
        return null;
    }

    private static bool IsLikelyTestPath(string rel)
    {
        var norm = rel.Replace('\\', '/');
        var low = norm.ToLowerInvariant();
        foreach (var seg in low.Split('/'))
        {
            if (seg is "test" or "tests" or "testing" or "e2e" or "it") return true;
            if (seg.EndsWith(".tests", StringComparison.Ordinal)
                || seg.EndsWith(".test", StringComparison.Ordinal)
                || seg.EndsWith(".unittests", StringComparison.Ordinal)
                || seg.EndsWith(".integrationtests", StringComparison.Ordinal)
                || seg.EndsWith(".specs", StringComparison.Ordinal)) return true;
        }
        // The capital is the word boundary: Contest.cs and LatestOrder.cs are production types.
        var stem = Path.GetFileNameWithoutExtension(norm);
        if (stem != "Test" && stem != "Tests"
            && (stem.EndsWith("Tests", StringComparison.Ordinal) || stem.EndsWith("Test", StringComparison.Ordinal)))
        {
            return true;
        }
        return stem.EndsWith("E2E", StringComparison.Ordinal);
    }

    // TestAttributeMarkers are the runner attributes that identify a test file by its CONTENT, for
    // a file whose path convention says nothing.
    private static readonly string[] TestAttributeMarkers =
    {
        "[Fact", "[Theory", "[Test", "[TestMethod", "[TestCase", "[DataTestMethod",
    };

    private static bool ContainsTestAttribute(string text)
    {
        foreach (var m in TestAttributeMarkers)
        {
            if (text.Contains(m, StringComparison.Ordinal)) return true;
        }
        return false;
    }

    private static (int sl, int el, int? sc, int? ec) LineSpan(SyntaxNode node)
    {
        var sp = node.GetLocation().GetLineSpan();
        var sl = sp.StartLinePosition.Line + 1;
        var el = sp.EndLinePosition.Line + 1;
        int? sc = sp.StartLinePosition.Character + 1;
        int? ec = sp.EndLinePosition.Character + 1;
        return (sl, el, sc, ec);
    }

    private static string CanonicalEdgeType(string raw)
    {
        var et = raw.Trim();
        if (string.IsNullOrEmpty(et))
        {
            return "";
        }
        return et.ToUpperInvariant() switch
        {
            "CALLS" => "CALLS",
            "IMPORTS" => "IMPORTS",
            "CONTAINS" => "CONTAINS",
            "EXTENDS" => "EXTENDS",
            "IMPLEMENTS" => "IMPLEMENTS",
            _ => et.ToUpperInvariant(),
        };
    }

    private static void EmitConstructorInjectionEdges(
        TypeDeclarationSyntax typeDecl,
        string typeFq,
        SemanticModel model,
        Action<string, string, string> addEdge)
    {
        var ctors = typeDecl.Members.OfType<ConstructorDeclarationSyntax>().ToList();
        if (ctors.Count == 0)
        {
            return;
        }

        IMethodSymbol? best = null;
        foreach (var ctor in ctors)
        {
            var cs = model.GetDeclaredSymbol(ctor);
            if (cs == null || cs.MethodKind != MethodKind.Constructor)
            {
                continue;
            }
            if (best == null || CompareConstructorPriority(cs, best) > 0)
            {
                best = cs;
            }
        }
        if (best == null)
        {
            return;
        }

        foreach (var p in best.Parameters)
        {
            var depFq = TypeFqNameOrDisplay(p.Type);
            if (!string.IsNullOrWhiteSpace(depFq))
            {
                if (IsNamedOrKeyedInjectionParameter(p))
                {
                    addEdge(typeFq, depFq, "INJECTS_NAMED");
                }
                else
                {
                    addEdge(typeFq, depFq, "INJECTS");
                }
            }
        }
    }

    private static bool IsNamedOrKeyedInjectionParameter(IParameterSymbol p)
    {
        foreach (var a in p.GetAttributes())
        {
            var n = a.AttributeClass?.Name ?? "";
            if (n.Equals("FromKeyedServicesAttribute", StringComparison.Ordinal) ||
                n.Equals("FromNamedServicesAttribute", StringComparison.Ordinal) ||
                n.Equals("ServiceKeyAttribute", StringComparison.Ordinal))
            {
                return true;
            }
            var fq = a.AttributeClass?.ToDisplayString(SymbolDisplayFormat.FullyQualifiedFormat) ?? "";
            if (fq.Contains("FromKeyedServicesAttribute", StringComparison.Ordinal) ||
                fq.Contains("FromNamedServicesAttribute", StringComparison.Ordinal) ||
                fq.Contains("ServiceKeyAttribute", StringComparison.Ordinal))
            {
                return true;
            }
        }
        return false;
    }

    private static int CompareConstructorPriority(IMethodSymbol a, IMethodSymbol b)
    {
        // Priority:
        // 1) [ActivatorUtilitiesConstructor]
        // 2) higher accessibility (public > internal/protected > private)
        // 3) more parameters
        var aAttr = HasActivatorUtilitiesCtorAttribute(a) ? 1 : 0;
        var bAttr = HasActivatorUtilitiesCtorAttribute(b) ? 1 : 0;
        if (aAttr != bAttr) return aAttr.CompareTo(bAttr);

        var aAcc = AccessibilityScore(a.DeclaredAccessibility);
        var bAcc = AccessibilityScore(b.DeclaredAccessibility);
        if (aAcc != bAcc) return aAcc.CompareTo(bAcc);

        return a.Parameters.Length.CompareTo(b.Parameters.Length);
    }

    private static bool HasActivatorUtilitiesCtorAttribute(IMethodSymbol ctor)
    {
        foreach (var attr in ctor.GetAttributes())
        {
            var n = attr.AttributeClass?.Name ?? "";
            if (n.Equals("ActivatorUtilitiesConstructorAttribute", StringComparison.Ordinal))
            {
                return true;
            }
            var fq = attr.AttributeClass?.ToDisplayString(SymbolDisplayFormat.FullyQualifiedFormat) ?? "";
            if (fq.Contains("Microsoft.Extensions.DependencyInjection.ActivatorUtilitiesConstructorAttribute", StringComparison.Ordinal))
            {
                return true;
            }
        }
        return false;
    }

    private static int AccessibilityScore(Accessibility a)
    {
        return a switch
        {
            Accessibility.Public => 4,
            Accessibility.ProtectedOrInternal => 3,
            Accessibility.Internal => 2,
            Accessibility.Protected => 2,
            Accessibility.Private => 1,
            _ => 0,
        };
    }

    private static void CollectServiceRegistrationEdges(
        SyntaxNode root,
        SemanticModel model,
        string moduleNs,
        Action<string, string, string> addEdge)
    {
        foreach (var inv in root.DescendantNodes().OfType<InvocationExpressionSyntax>())
        {
            var sym = model.GetSymbolInfo(inv).Symbol as IMethodSymbol;
            if (sym == null)
            {
                continue;
            }
            var name = sym.Name;
            if (!IsServiceRegistrationMethod(name))
            {
                continue;
            }

            var regCaller = FindEnclosingCallableFq(inv, model);
            if (string.IsNullOrWhiteSpace(regCaller))
            {
                regCaller = moduleNs;
            }
            if (string.IsNullOrWhiteSpace(regCaller))
            {
                continue;
            }

            ITypeSymbol? serviceType = null;
            ITypeSymbol? implType = null;

            if (sym.TypeArguments.Length >= 2)
            {
                serviceType = sym.TypeArguments[0];
                implType = sym.TypeArguments[1];
            }
            else if (sym.TypeArguments.Length == 1)
            {
                serviceType = sym.TypeArguments[0];
            }

            if (serviceType == null || implType == null)
            {
                ResolveServiceAndImplementationTypesFromArguments(inv, model, ref serviceType, ref implType);
            }
            if (implType == null)
            {
                implType = ResolveImplementationTypeFromFactory(inv, model);
            }

            var serviceFq = TypeFqNameOrDisplay(serviceType);
            var implFq = TypeFqNameOrDisplay(implType);

            if (!string.IsNullOrWhiteSpace(serviceFq))
            {
                addEdge(regCaller, serviceFq, "REGISTERS_SERVICE");
            }
            if (!string.IsNullOrWhiteSpace(implFq))
            {
                addEdge(regCaller, implFq, "REGISTERS_SERVICE");
            }
            if (!string.IsNullOrWhiteSpace(serviceFq) && !string.IsNullOrWhiteSpace(implFq) && !string.Equals(serviceFq, implFq, StringComparison.Ordinal))
            {
                addEdge(implFq, serviceFq, "IMPLEMENTS_SERVICE");
            }
        }
    }

    private static bool IsServiceRegistrationMethod(string name)
    {
        return name is "AddScoped" or "AddTransient" or "AddSingleton" or
               "AddKeyedScoped" or "AddKeyedTransient" or "AddKeyedSingleton" or
               "TryAdd" or "TryAddScoped" or "TryAddTransient" or "TryAddSingleton";
    }

    private static void ResolveServiceAndImplementationTypesFromArguments(
        InvocationExpressionSyntax inv,
        SemanticModel model,
        ref ITypeSymbol? serviceType,
        ref ITypeSymbol? implType)
    {
        var typeArgs = new List<ITypeSymbol>();
        foreach (var a in inv.ArgumentList.Arguments)
        {
            if (a.Expression is TypeOfExpressionSyntax toe)
            {
                var t = model.GetTypeInfo(toe.Type).Type;
                if (t != null)
                {
                    typeArgs.Add(t);
                }
            }
        }
        if (serviceType == null && typeArgs.Count >= 1)
        {
            serviceType = typeArgs[0];
        }
        if (implType == null && typeArgs.Count >= 2)
        {
            implType = typeArgs[1];
        }
    }

    private static ITypeSymbol? ResolveImplementationTypeFromFactory(InvocationExpressionSyntax inv, SemanticModel model)
    {
        foreach (var a in inv.ArgumentList.Arguments)
        {
            var expr = a.Expression;
            if (expr is ParenthesizedLambdaExpressionSyntax pl)
            {
                if (pl.Body is ExpressionSyntax be)
                {
                    var t = model.GetTypeInfo(be).Type;
                    if (t != null)
                    {
                        return t;
                    }
                }
            }
            if (expr is SimpleLambdaExpressionSyntax sl)
            {
                if (sl.Body is ExpressionSyntax be)
                {
                    var t = model.GetTypeInfo(be).Type;
                    if (t != null)
                    {
                        return t;
                    }
                }
            }
        }
        return null;
    }

    private static string TypeFqNameOrDisplay(ITypeSymbol? t)
    {
        if (t == null)
        {
            return "";
        }
        if (t is INamedTypeSymbol nts)
        {
            return TypeFqName(nts);
        }
        var s = t.ToDisplayString(SymbolDisplayFormat.FullyQualifiedFormat).Trim();
        if (s.StartsWith("global::", StringComparison.Ordinal))
        {
            s = s.Substring("global::".Length);
        }
        return s;
    }

    private static void EmitCallableTypeSurfaceEdges(
        IMethodSymbol methodSymbol,
        string callerFq,
        Action<string, string, string> addEdge,
        bool includeReturnType)
    {
        foreach (var p in methodSymbol.Parameters)
        {
            var paramTypeFq = TypeFqNameOrDisplay(p.Type);
            if (!string.IsNullOrWhiteSpace(paramTypeFq))
            {
                addEdge(callerFq, paramTypeFq, "ACCEPTS_PARAM_TYPE");
            }
        }
        if (includeReturnType && methodSymbol.MethodKind == MethodKind.Ordinary)
        {
            var ret = methodSymbol.ReturnType;
            if (ret.SpecialType != SpecialType.System_Void)
            {
                var returnTypeFq = TypeFqNameOrDisplay(ret);
                if (!string.IsNullOrWhiteSpace(returnTypeFq))
                {
                    addEdge(callerFq, returnTypeFq, "RETURNS_TYPE");
                }
            }
        }
    }

    private static void EmitFieldAccessEdges(
        SyntaxNode bodyOwner,
        SemanticModel model,
        string callerFq,
        Action<string, string, string> addEdge)
    {
        var writeSpans = new HashSet<string>(StringComparer.Ordinal);

        void MarkWrite(ExpressionSyntax expr)
        {
            var field = ResolveFieldSymbol(expr, model);
            if (field == null) return;
            var fieldFq = FieldFqName(field);
            if (string.IsNullOrWhiteSpace(fieldFq)) return;
            addEdge(callerFq, fieldFq, "WRITES_FIELD");
            writeSpans.Add(expr.SpanStart + ":" + expr.Span.Length);
        }

        foreach (var a in bodyOwner.DescendantNodes().OfType<AssignmentExpressionSyntax>())
        {
            MarkWrite(a.Left);
        }
        foreach (var pp in bodyOwner.DescendantNodes().OfType<PostfixUnaryExpressionSyntax>())
        {
            if (pp.IsKind(SyntaxKind.PostIncrementExpression) || pp.IsKind(SyntaxKind.PostDecrementExpression))
            {
                MarkWrite(pp.Operand);
            }
        }
        foreach (var pu in bodyOwner.DescendantNodes().OfType<PrefixUnaryExpressionSyntax>())
        {
            if (pu.IsKind(SyntaxKind.PreIncrementExpression) || pu.IsKind(SyntaxKind.PreDecrementExpression))
            {
                MarkWrite(pu.Operand);
            }
        }

        void TryRead(ExpressionSyntax expr)
        {
            var key = expr.SpanStart + ":" + expr.Span.Length;
            if (writeSpans.Contains(key))
            {
                return;
            }
            var field = ResolveFieldSymbol(expr, model);
            if (field == null) return;
            var fieldFq = FieldFqName(field);
            if (!string.IsNullOrWhiteSpace(fieldFq))
            {
                addEdge(callerFq, fieldFq, "READS_FIELD");
            }
        }

        foreach (var id in bodyOwner.DescendantNodes().OfType<IdentifierNameSyntax>())
        {
            TryRead(id);
        }
        foreach (var ma in bodyOwner.DescendantNodes().OfType<MemberAccessExpressionSyntax>())
        {
            TryRead(ma);
        }
    }

    private static IFieldSymbol? ResolveFieldSymbol(ExpressionSyntax expr, SemanticModel model)
    {
        var sym = model.GetSymbolInfo(expr).Symbol;
        if (sym is IFieldSymbol fs && !fs.IsImplicitlyDeclared)
        {
            return fs;
        }
        return null;
    }

    private static string FieldFqName(IFieldSymbol field)
    {
        return TypeFqName(field.ContainingType) + "#" + field.Name;
    }

    private static void IndexProperty(PropertyDeclarationSyntax pd, string typeFq, SemanticModel model,
        List<SymbolDto> symbols, Action<string, string, string> addEdge)
    {
        var ps = model.GetDeclaredSymbol(pd) as IPropertySymbol;
        if (ps == null) return;
        var fq = typeFq + "#" + ps.Name;
        var (sl, el, sc, ec) = LineSpan(pd);
        symbols.Add(new SymbolDto
        {
            Kind = "property",
            FqName = fq,
            StartLine = sl,
            EndLine = el,
            StartColumn = sc,
            EndColumn = ec,
            Signature = BuildMemberSignature(ps, ps.IsStatic),
        });
        addEdge(typeFq, fq, "CONTAINS");
    }

    private static void IndexEvent(EventDeclarationSyntax ed, string typeFq, SemanticModel model,
        List<SymbolDto> symbols, Action<string, string, string> addEdge)
    {
        var es = model.GetDeclaredSymbol(ed) as IEventSymbol;
        if (es == null) return;
        var fq = typeFq + "#" + es.Name;
        var (sl, el, sc, ec) = LineSpan(ed);
        symbols.Add(new SymbolDto
        {
            Kind = "event",
            FqName = fq,
            StartLine = sl,
            EndLine = el,
            StartColumn = sc,
            EndColumn = ec,
            Signature = BuildMemberSignature(es, es.IsStatic),
        });
        addEdge(typeFq, fq, "CONTAINS");
    }

    private static void IndexEventField(EventFieldDeclarationSyntax efd, string typeFq, SemanticModel model,
        List<SymbolDto> symbols, Action<string, string, string> addEdge)
    {
        foreach (var v in efd.Declaration.Variables)
        {
            var es = model.GetDeclaredSymbol(v) as IEventSymbol;
            if (es == null) continue;
            var fq = typeFq + "#" + es.Name;
            var (sl, el, sc, ec) = LineSpan(v);
            symbols.Add(new SymbolDto
            {
                Kind = "event",
                FqName = fq,
                StartLine = sl,
                EndLine = el,
                StartColumn = sc,
                EndColumn = ec,
                Signature = BuildMemberSignature(es, es.IsStatic),
            });
            addEdge(typeFq, fq, "CONTAINS");
        }
    }
}

internal sealed class LangIndexerDoc
{
    [JsonPropertyName("path")] public string Path { get; set; } = "";
    [JsonPropertyName("lang")] public string Lang { get; set; } = "csharp";
    [JsonPropertyName("module")] public string Module { get; set; } = "";
    [JsonPropertyName("is_test")] public bool IsTest { get; set; }
    [JsonPropertyName("symbols")] public List<SymbolDto> Symbols { get; set; } = new();
    [JsonPropertyName("edges")] public List<EdgeDto> Edges { get; set; } = new();
    // Invocations whose target symbol did not resolve in this file (null on zero) — the per-file
    // slice of indexer.csharp_unresolved_invocations.
    [JsonPropertyName("unresolved_invocations")] public int? UnresolvedInvocations { get; set; }
}

internal sealed class SymbolDto
{
    [JsonPropertyName("kind")] public string Kind { get; set; } = "";
    [JsonPropertyName("fq_name")] public string FqName { get; set; } = "";
    [JsonPropertyName("start_line")] public int StartLine { get; set; }
    [JsonPropertyName("end_line")] public int EndLine { get; set; }
    [JsonPropertyName("start_column")] public int? StartColumn { get; set; }
    [JsonPropertyName("end_column")] public int? EndColumn { get; set; }
    [JsonPropertyName("signature")] public JsonElement? Signature { get; set; }
}

internal sealed class EdgeDto
{
    [JsonPropertyName("caller_fq_name")] public string CallerFqName { get; set; } = "";
    [JsonPropertyName("callee_fq_name")] public string CalleeFqName { get; set; } = "";
    [JsonPropertyName("edge_type")] public string EdgeType { get; set; } = "";
}
