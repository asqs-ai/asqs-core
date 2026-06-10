import * as fs from "fs";
import * as os from "os";
import * as path from "path";
import { describe, expect, it } from "vitest";
import { Project } from "ts-morph";
import { enrichAngularTemplateAst } from "./enrichers-angular-template-ast";
import type { FileSymbolsEdges } from "./enrichers";

describe("enrichAngularTemplateAst", () => {
  it("reads templateUrl HTML and emits ANGULAR_TEMPLATE_BINDING symbols", () => {
    const root = fs.mkdtempSync(path.join(os.tmpdir(), "ngtpl-"));
    fs.writeFileSync(
      path.join(root, "app.component.html"),
      `<button (click)="save()">Go</button>
<div *ngIf="show">{{ name | uppercase }}</div>
<input [(ngModel)]="x" />`,
      "utf-8",
    );
    fs.writeFileSync(
      path.join(root, "app.component.ts"),
      `import { Component } from '@angular/core';
@Component({ selector: 'app-root', templateUrl: './app.component.html' })
export class AppComponent {}
`,
      "utf-8",
    );

    const project = new Project({ compilerOptions: { target: 99 }, skipAddingFilesFromTsConfig: true });
    project.addSourceFileAtPath(path.join(root, "app.component.ts"));
    const sf = project.getSourceFileOrThrow(path.join(root, "app.component.ts"));
    const entry: FileSymbolsEdges = { symbols: [], edges: [] };
    enrichAngularTemplateAst(sf, entry, "app.mod");

    const bindings = entry.symbols.filter((s) => s.kind === "ANGULAR_TEMPLATE_BINDING");
    expect(bindings.length).toBeGreaterThan(0);
    expect(bindings.some((b) => b.signature && (b.signature as { name: string }).name === "click")).toBe(true);
    expect(bindings.some((b) => b.signature && (b.signature as { name: string }).name === "ngIf")).toBe(true);
    const tplEdge = entry.edges.find(
      (e) => e.caller_fq_name === "ANGULAR_TEMPLATE:./app.component.html" && e.edge_type === "CONTAINS",
    );
    expect(tplEdge).toBeDefined();
  });
});
