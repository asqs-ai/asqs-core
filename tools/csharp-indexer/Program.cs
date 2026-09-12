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

        if (isTest && text.Contains("Microsoft.Playwright", StringComparison.Ordinal))
        {
            var (sl, el, _, _) = LineSpan(root);
            symbols.Add(new SymbolDto
            {
                Kind = "E2E_SPEC",
                FqName = relPath + "#e2e",
                StartLine = sl,
                EndLine = el,
                Signature = JsonSerializer.SerializeToElement(new { framework = "playwright-dotnet" }),
            });
        }

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

    private static void ExtractAspNetRoutes(MethodDeclarationSyntax md, string typeFq, SemanticModel model,
        List<SymbolDto> symbols, List<EdgeDto> edges)
    {
        var ms = model.GetDeclaredSymbol(md);
        if (ms == null) return;
        string? classTemplate = null;
        var typeDecl = md.Ancestors().OfType<TypeDeclarationSyntax>().FirstOrDefault();
        if (typeDecl != null)
        {
            var tsym = model.GetDeclaredSymbol(typeDecl) as INamedTypeSymbol;
            if (tsym != null)
                classTemplate = GetRouteTemplate(tsym.GetAttributes());
        }

        foreach (var attr in ms.GetAttributes())
        {
            var cn = attr.AttributeClass?.Name;
            if (cn is null) continue;
            string? http = cn switch
            {
                "HttpGetAttribute" => "GET",
                "HttpPostAttribute" => "POST",
                "HttpPutAttribute" => "PUT",
                "HttpDeleteAttribute" => "DELETE",
                "HttpPatchAttribute" => "PATCH",
                _ => null,
            };
            if (http == null) continue;
            var tmpl = GetTemplateFromAttribute(attr) ?? "";
            var path = CombineRoute(classTemplate, tmpl);
            if (string.IsNullOrEmpty(path)) continue;
            var handlerFq = MethodFqName(ms);
            var routeFq = $"API_ROUTE:{http}:{path}@{handlerFq}";
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

    private static void CollectHttpClientRequests(SyntaxNode root, SemanticModel model, List<SymbolDto> symbols, List<EdgeDto> edges)
    {
        foreach (var inv in root.DescendantNodes().OfType<InvocationExpressionSyntax>())
        {
            if (inv.Expression is not MemberAccessExpressionSyntax ma) continue;
            var name = ma.Name.Identifier.Text;
            if (name is not ("GetAsync" or "PostAsync" or "PutAsync" or "DeleteAsync" or "SendAsync")) continue;
            var sym = model.GetSymbolInfo(inv).Symbol as IMethodSymbol;
            if (sym == null) continue;
            var containing = sym.ContainingType.ToDisplayString(SymbolDisplayFormat.FullyQualifiedFormat);
            if (!containing.Contains("System.Net.Http.HttpClient", StringComparison.Ordinal)) continue;

            string? path = null;
            var method = name switch
            {
                "GetAsync" => "GET",
                "PostAsync" => "POST",
                "PutAsync" => "PUT",
                "DeleteAsync" => "DELETE",
                _ => "GET",
            };
            if (inv.ArgumentList.Arguments.Count > 0)
            {
                var arg0 = inv.ArgumentList.Arguments[0].Expression;
                path = TryGetStringConstant(model, arg0);
            }

            if (string.IsNullOrEmpty(path)) continue;

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

    private static bool IsLikelyTestPath(string rel)
    {
        var low = rel.Replace('\\', '/').ToLowerInvariant();
        if (low.Contains("/test/") || low.Contains("/tests/")) return true;
        if (low.Contains(".tests.")) return true;
        if (Path.GetFileNameWithoutExtension(rel).EndsWith("Tests", StringComparison.Ordinal)) return true;
        if (low.Contains("/e2e/") || low.StartsWith("e2e/", StringComparison.Ordinal)) return true;
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
