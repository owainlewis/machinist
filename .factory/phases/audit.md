---
name: audit
description: Find real defects in a repository and file one issue for each
runtime: claude
timeout: 1h
permissions: read-only
max_findings: 5
---
Audit {{ run.repo }} for real defects.

Look for incorrect logic, unhandled errors that lose data, race conditions,
resource leaks, and security issues. Read the code. Do not infer defects from
names or comments.

For each finding you are confident in, file one issue containing:

- a title that states the defect, not the area it lives in
- the file and line
- a concrete failure scenario: specific inputs or state, then the wrong result
- acceptance criteria that a later phase can check against a diff

Rules:

- File at most {{ phase.max_findings }} issues. Rank by severity and file the
  most severe.
- If you cannot write a concrete failure scenario, it is not a finding. Drop it.
- Label every issue `factory`.
- Style, naming, and refactors are not defects. Do not file them.
- Change no code.
