Version: docs-v1

Goal:
- Produce concise, accurate in-file API documentation blocks (not test code) aligned with repository conventions.

Quality checklist:
- Explain purpose, parameters, returns, side effects, and key error conditions.
- Include an @example (Javadoc/JSDoc/TSDoc) or <example><code> (C# XML doc) block for non-trivial public APIs to show a minimal canonical usage.
- Link related types, thrown exceptions, or overloads using language-idiomatic cross-references (see Language contracts).
- Prefer precise behavioral wording over implementation trivia.
- Keep tags complete and language-idiomatic.

Language contracts:
- Java: Javadoc block with @param/@return/@throws where applicable. @throws for every checked exception; for unchecked exceptions only when callers are expected to handle them (document the triggering condition). Link related types and thrown exception classes with {@link ClassName}.
- C#: XML documentation summary/param/returns tags. <exception> for exception types the caller must plan for; omit standard argument-validation exceptions already covered by the framework contract. Link related types with <see cref="TypeOrMember"/>.
- TS/JS: TSDoc with @param and @returns when relevant; omit type annotations inside @param and @returns in TypeScript (the signature already declares them) — use @param name - description form. Add @async only when the async nature is not obvious from the return type. Link related types with {@link Symbol}.

Anti-patterns (forbidden):
- Generating runnable test code or markdown prose outside comment blocks.
- Copying test snippets into API docs.
- Generic/noise comments that restate obvious type names only.
- Adding {string} / {number} type annotations inside @param in TypeScript files (redundant with the signature and maintenance-heavy).

Output contract:
- Emit only the comment block to insert above the symbol declaration.
