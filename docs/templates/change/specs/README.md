# Future-State Spec Copies

> This directory mirrors `docs/specs/` paths. Each file here is the **complete, amended version** of the spec file it replaces — exactly as it should read after this change is synced.

```
specs/
└── NN-component/
    └── NN-feature.md     ← copy of docs/specs/NN-component/NN-feature.md, amended in place
```

## Rules

- Copy the affected file from `docs/specs/` into the same path here, then apply the amendment (add subfeatures, modify requirements, remove sections).
- The file must be the FULL future state — no delta markers, no `ADDED`/`MODIFIED` headers. The git diff between this file and the current one in `docs/specs/` IS the change.
- On `specs-sync`: copy these files back over `docs/specs/`, append any new subfeature codes to the file's `spec-refs` frontmatter array, update Status History, regenerate the index.
- Multiple affected files → mirror each one at its path.

## Why

Reviewing a change means reading the spec exactly as it will read after archive — no mental merge. The sync step is a mechanical file replacement, so agents never parse delta syntax.