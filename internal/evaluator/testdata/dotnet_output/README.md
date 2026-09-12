# Real `dotnet` tool output

Captured from the .NET 10.0.201 CLI against a throwaway xUnit project, with the host path rewritten
to `/workspace` (the container mount point ASQS evaluates under). These drive the C# diagnostic
patterns in `errloc`, `errout` and `errclass` — the shapes are transcribed from a real run rather
than written from memory, because every one of them has a detail that matters:

- `vstest_failures.txt` — frames are `at Ns.Type.Method(Int32 a, Decimal b) in <path>:line <n>`,
  framework frames carry no `in <path>` at all, a `[Theory]` failure's display name includes its
  arguments, and the summary is `Total tests: N` / `     Failed: N` with leading spaces.
- `msbuild_failed.txt` — every diagnostic is printed TWICE, once inline and once in the recap after
  `Build FAILED.`, followed by `N Warning(s) / M Error(s)` and `Time Elapsed`.
- `vstest_no_tests.txt` — `No test is available in <dll>. Make sure that test discoverer & executors
  are registered ...`, which exits non-zero and is not a failure this run can repair.
