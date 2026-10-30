# Identifier details

Use mixedCaps and consistent initialisms (`ID`, `URL`, `HTTP`, `JSON`). Keep short names for short scopes and descriptive names for values that cross boundaries or have similar peers. Avoid names that encode a type (`userMap`, `strValue`) or a vague role (`data`, `thing`, `helper`) when the domain term is available.

Avoid stutter and unnecessary abbreviations. A package-qualified name should read naturally (`http.Client`, `bytes.Buffer`). Do not shadow imported packages, predeclared identifiers, or an outer value when it makes the code ambiguous; short loop variables are fine in a tiny loop.

Boolean names should read as a question. Prefer `Enabled` to `IsNotDisabled`, and `HasItems` to `ItemsExist` when the package’s vocabulary supports it. Keep exported names documented and do not rename public identifiers without considering source compatibility.
