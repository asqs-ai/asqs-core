## Access-modifier compile failures: pick a verify-side-effect test instead of forcing access

The last compile diagnostic says the test is trying to invoke a member that is **not visible** at the call site. Typical shapes:

- Java / JVM — `getFoo() has protected access in com.example.Bar`, `getFoo() is not public in com.example.Bar; cannot be accessed from outside package`, `Baz has package-private access`.
- C# / .NET — `CS0122 'Foo.Bar(...)' is inaccessible due to its protection level`, `CS0103` on an internal-only type, `CS0272` on a private setter.

**Do not** fix this by changing the production class's access modifier (you cannot edit production code from the fixer), by copy-pasting the member into the test file, or by faking a subclass that "exposes" it (subclassing does not bypass `protected` from another package, and it does not reproduce the real runtime wiring).

### Strategy 1 — verify side effects instead of reading private state (preferred)

If the production component is invoked through a **public entry point** (controller, configurer, `@Bean`, etc.), drive it through that entry point and assert the **observable** outcome: a registry got populated, a filter chain saw a request, a mock dependency received a call with expected arguments. That's a real behavioural test that does not need access to the internal accessor at all.

- **Java (Spring MVC example)**: do not call `registry.getInterceptors()` (protected). Instead,
  - `InterceptorRegistry registry = Mockito.spy(new InterceptorRegistry());` then invoke the production `addInterceptors(registry)` and `verify(registry).addInterceptor(any())` — captures the registration side-effect using the public API.
  - Or stand up a minimal `MockMvc` / `WebApplicationContext` and `mockMvc.perform(...)` through the configured filter chain; assert the observable behavior (response header set, request logged, etc.).
- **Java (Spring WebConfig example)**: for `InterceptorRegistry.getInterceptors()` / `InterceptorRegistration.getInterceptor()` — both `protected` from an outside package — drive the registration through `addInterceptors(new InterceptorRegistry())` on a spy, then assert on the mock/spy invocations (`verify(registry, times(1)).addInterceptor(any(HandlerInterceptor.class))`), not on the registry internals.
- **C# (ASP.NET Core example)**: do not read a private `_options` field. Build the real service collection, call the extension under test, then `Assert.Contains(services, s => s.ServiceType == typeof(IFoo) && s.Lifetime == ServiceLifetime.Singleton)`.

### Strategy 2 — reflection helpers as an explicit, local break-glass

If you genuinely need to read a `protected`/`package-private` accessor to assert a post-condition and the repo's existing tests already use a reflection helper, use the **same helper** rather than introducing a new pattern.

- **Java (Spring Test)**: `ReflectionTestUtils.invokeMethod(obj, "getFoo")` or `ReflectionTestUtils.getField(obj, "foo")`. Import `org.springframework.test.util.ReflectionTestUtils`. Only use this when a behavioural assertion (Strategy 1) is genuinely infeasible, and leave a one-line comment explaining why.
- **Java (no Spring)**: `var m = target.getClass().getDeclaredMethod("getFoo"); m.setAccessible(true); Object v = m.invoke(target);` — wrap in a helper method, document the reason.
- **C#**: `typeof(Foo).GetMethod("Bar", BindingFlags.NonPublic | BindingFlags.Instance).Invoke(instance, new object[] { })`. Cast the result. For internals in another assembly, check whether the repo already has an `InternalsVisibleTo` attribute pointing at the test assembly; if yes, a plain call works and reflection is not needed.

### Strategy 3 — if the member is truly unreachable, remove the assertion, do not fake it

If neither Strategy 1 nor Strategy 2 applies (no public entry point, no reflection helper in use, no `InternalsVisibleTo`), the test is asserting against an internal you cannot observe. **Do not** replace the failing call with a tautology (`assertTrue(true)`), a type-metadata check (`assertNotNull(Foo.class)`), or an empty body — that destroys coverage. Prefer one of:

1. Re-scope the test to a different public behaviour of the same production component that is still worth asserting.
2. Disable only that single test method with the repo's existing skip annotation and a one-line honest reason (e.g. `@Disabled("asserts protected accessor; replaced by behavioural test in FooIntegrationIT")`). Leave other test methods in the file untouched.

### Common anti-patterns to avoid on this error class

- Swapping `getInterceptors()` for `equals`/`toString`/`hashCode` of the registry — not a test of behaviour.
- Wrapping the call in a `try { … } catch (Exception e) { }` to make it "pass".
- Catching the compile error by deleting the test method entirely.
- Declaring the test class in the same package as the production class just to gain `protected` / package-private access — breaks the project layout and usually still fails because Maven/Gradle source sets disagree.
