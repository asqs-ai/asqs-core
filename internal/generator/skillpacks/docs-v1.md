Version: docs-v1

Goal:
- Produce concise, accurate in-file API documentation blocks (not test code) aligned with repository conventions.

Quality checklist:
- Explain purpose, parameters, returns, side effects, and key error conditions.
- Prefer precise behavioral wording over implementation trivia.
- Keep tags complete and language-idiomatic.

Language contracts:
- Java: Javadoc block with @param/@return/@throws where applicable.
- C#: XML documentation summary/param/returns tags.
- TS/JS: JSDoc/TSDoc with @param/@returns/@async when relevant.

Anti-patterns (forbidden):
- Generating runnable test code or markdown prose outside comment blocks.
- Copying test snippets into API docs.
- Generic/noise comments that restate obvious type names only.

Output contract:
- Emit only the comment block to insert above the symbol declaration.
