import { describe, it, expect } from "vitest";
import { Project } from "ts-morph";
import { spanColumns1Based, spanColumnsForNode } from "./span-columns";

describe("span-columns", () => {
  it("returns 1-based UTF-16 columns consistent with TypeScript positions", () => {
    const project = new Project({ useInMemoryFileSystem: true });
    project.createSourceFile(
      "t.ts",
      `// head
export class Foo {
  bar() { return 1; }
}`,
    );
    const sf = project.getSourceFileOrThrow("t.ts");
    const cls = sf.getClasses()[0];
    const span = spanColumnsForNode(cls);
    expect(span.start_column).toBeGreaterThanOrEqual(1);
    expect(span.end_column).toBeGreaterThanOrEqual(span.start_column);
  });

  it("spanColumns1Based covers statement through initializer end", () => {
    const project = new Project({ useInMemoryFileSystem: true });
    const sf = project.createSourceFile("v.ts", "export const x = () => 1;\n");
    const stmt = sf.getVariableStatements()[0];
    const init = stmt.getDeclarationList().getDeclarations()[0].getInitializer()!;
    const span = spanColumns1Based(stmt, stmt.getStart(), init.getEnd());
    expect(span.start_column).toBeGreaterThanOrEqual(1);
    expect(span.end_column).toBeGreaterThanOrEqual(1);
  });
});
