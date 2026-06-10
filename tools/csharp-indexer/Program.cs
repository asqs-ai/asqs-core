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

        foreach (var file in EnumerateCsFiles(repo))
        {
            var rel = Path.GetRelativePath(repo, file).Replace('\\', '/');
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
                var root = tree.GetRoot();

                var doc = IndexFile(rel, text, root, model);
                var line = JsonSerializer.Serialize(doc, JsonOpts);
                Console.WriteLine(line);
            }
            catch (Exception ex)
            {
                Console.Error.WriteLine($"csharp-indexer: skip {rel}: {ex.Message}");
            }
        }

        return 0;
    }

    private static IEnumerable<string> EnumerateCsFiles(string repo)
    {
        foreach (var path in Directory.EnumerateFiles(repo, "*.cs", SearchOption.AllDirectories))
        {
            var norm = path.Replace('\\', '/');
            if (norm.Contains("/bin/", StringComparison.OrdinalIgnoreCase)) continue;
            if (norm.Contains("/obj/", StringComparison.OrdinalIgnoreCase)) continue;
            if (norm.Contains("/.vs/", StringComparison.OrdinalIgnoreCase)) continue;
            yield return path;
        }
    }

    private static LangIndexerDoc IndexFile(string relPath, string text, SyntaxNode root, SemanticModel model)
    {
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
            if (u.StaticKeyword.IsKind(SyntaxKind.StaticKeyword)) continue;
            var name = u.Name?.ToString().Trim();
            if (string.IsNullOrEmpty(name)) continue;
            var line = u.GetLocation().GetLineSpan().StartLinePosition.Line + 1;
            if (!string.IsNullOrEmpty(moduleNs))
            {
                AddEdge(moduleNs, name, "IMPORTS");
            }
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
                Signature = BuildTypeSignature(sym),
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
                }
            }

            // DI extraction: constructor injection edges for the most activatable constructor.
            EmitConstructorInjectionEdges(typeDecl, typeFq, model, AddEdge);

            CollectInvocations(typeDecl, model, edges);
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

        return new LangIndexerDoc
        {
            Path = relPath,
            Lang = "csharp",
            Module = moduleNs ?? "",
            IsTest = isTest,
            Symbols = symbols,
            Edges = edges,
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
            Signature = BuildMethodSignature(ms),
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
        var fq = typeFq + "#.ctor";
        var (sl, el, sc, ec) = LineSpan(cd);
        symbols.Add(new SymbolDto
        {
            Kind = "constructor",
            FqName = fq,
            StartLine = sl,
            EndLine = el,
            StartColumn = sc,
            EndColumn = ec,
            Signature = BuildMethodSignature(cs),
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

    private static void CollectInvocations(SyntaxNode scope, SemanticModel model, List<EdgeDto> edges)
    {
        foreach (var inv in scope.DescendantNodes().OfType<InvocationExpressionSyntax>())
        {
            var callerMethodFq = FindEnclosingCallableFq(inv, model);
            if (string.IsNullOrEmpty(callerMethodFq)) continue;

            var sym = model.GetSymbolInfo(inv).Symbol;
            if (sym is IMethodSymbol called)
            {
                var callee = MethodFqName(called);
                if (!string.IsNullOrEmpty(callee))
                    edges.Add(new EdgeDto { CallerFqName = callerMethodFq, CalleeFqName = callee, EdgeType = "CALLS" });
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
                return s != null ? TypeFqName(s.ContainingType) + "#.ctor" : null;
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

    private static string MethodFqName(IMethodSymbol m)
    {
        var typeFq = TypeFqName(m.ContainingType);
        return typeFq + "#" + m.Name;
    }

    private static string TypeFqName(INamedTypeSymbol t)
    {
        var fmt = new SymbolDisplayFormat(
            globalNamespaceStyle: SymbolDisplayGlobalNamespaceStyle.Omitted,
            typeQualificationStyle: SymbolDisplayTypeQualificationStyle.NameAndContainingTypesAndNamespaces,
            genericsOptions: SymbolDisplayGenericsOptions.None,
            miscellaneousOptions: SymbolDisplayMiscellaneousOptions.EscapeKeywordIdentifiers);
        return t.ToDisplayString(fmt);
    }

    private static JsonElement? BuildTypeSignature(INamedTypeSymbol t)
    {
        return JsonSerializer.SerializeToElement(new
        {
            visibility = t.DeclaredAccessibility.ToString().ToLowerInvariant(),
        });
    }

    private static JsonElement? BuildMethodSignature(IMethodSymbol m)
    {
        return JsonSerializer.SerializeToElement(new Dictionary<string, object>
        {
            ["visibility"] = m.DeclaredAccessibility.ToString().ToLowerInvariant(),
            ["static"] = m.IsStatic,
        });
    }

    private static JsonElement? BuildMemberSignature(ISymbol s, bool isStatic)
    {
        return JsonSerializer.SerializeToElement(new Dictionary<string, object>
        {
            ["visibility"] = s.DeclaredAccessibility.ToString().ToLowerInvariant(),
            ["static"] = isStatic,
        });
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
