# A2UI implementation notes

This engine implements A2UI v1.0 (primary, per
`specification/v1_0/docs/a2ui_protocol.md` of a2ui-project/a2ui) with
wire-level tolerance for v0.9 / v0.9.1. Where the two disagree, v1.0
semantics win; cheap v0.9.1 tolerance is kept and listed here.

## Wire-level differences between v0.9(.1) and v1.0

| Topic | v0.9.1 | v1.0 | This engine |
| --- | --- | --- | --- |
| Envelope version | enum `v0.9`,`v0.9.1` | const `v1.0` | accepts all three; unknown versions apply best-effort with `ErrUnknownVersion` recorded |
| `createSurface` inline tree | not allowed (surfaceId + catalogId[required] + theme + sendDataModel) | `components` + `dataModel` inline; `catalogId` optional | inline fields accepted for both |
| `theme` property | present | removed | ignored on decode |
| `updateDataModel.value` | optional; omitted = delete key | required; `null` = delete key | both map to delete; `HasValue` distinguishes omission from null |
| Path `/` semantics | same | same | whole-model replace (see below — NOT raw RFC 6901) |
| Text `variant` | `h1..h5, caption, body` | `caption, body` | all accepted; h1-h5 render emphasized |
| CheckRule | `condition` is DynamicBoolean, `message` required | `condition` is path/function returning ValidationResult, `message` optional fallback | both accepted; booleans coerce to `{valid: b}` |
| FunctionCall `returnType` | optional hint field | gone (moved to catalog metadata) | tolerated, ignored |
| RPC pairs | none | `callRendererFunction`, `agentFunctionResponse` (+ renderer→agent `callAgentFunction`, `rendererFunctionResponse`) | decoded; static engine records callRendererFunction as an error entry (interactive milestone will execute) |
| Component `catalogId` | not on components | per-component override | decoded |

## Spec details that differ from common assumptions

- **`updateDataModel` path `/` means the whole model.** "If omitted (or is
  `/`), the entire data model is replaced." In raw RFC 6901, `/` is the
  empty *key*. The engine special-cases `""` and `"/"` to whole-model
  replace/delete before handing anything else to the JSON Pointer engine.
- **Validation functions return ValidationResult objects (v1.0), not
  booleans.** `required`, `regex`, `length`, `numeric`, `email` return
  `{valid: bool, ...}`. Only `and`/`or`/`not` return booleans. Because the
  spec's own button-validation example nests `required` calls inside
  `and.args.values`, boolean coercion treats `{valid: false}` as false.
- **Basic validators are message-free.** The spec's contact-form example
  supplies human text via CheckRule.message; per v1.0 that message is a
  "fallback", so this engine's built-in validators return bare
  `{valid:false}` and `CheckDisplayMessage` prefers the rule's message (a
  ValidationResult carrying its own message still wins, e.g. from custom
  catalog functions).
- **Checks appear in two wire shapes.** The schema says
  `{condition: ..., message}`; the spec's worked examples send bare function
  calls `{call, args, message}`. Both decode; the bare form is the entire
  object minus `message` as the condition.
- **formatDate tokens are TR35**, catalog-documented subset: yy, yyyy, M, MM,
  MMM, MMMM, d, dd, E, EEEE, h, hh, H, HH, mm, ss, a (there is no EEE).
  The spec's own example uses `E MMM d, YYYY h:mm a` with uppercase YYYY, so
  YYYY/DD are tolerated as yyyy/dd. Unsupported token runs pass through
  verbatim (visible degradation, never a panic). Unparseable date values
  return the raw string (progressive rendering).
- **pluralize does not substitute the count.** The implementation guide maps
  the CLDR category to a string and falls back to `other`; no `{count}`
  templating exists. CLDR categories are computed with real cardinal rules
  from `golang.org/x/text/feature/plural` under `language.English` (English
  has no `zero` cardinal rule: 0 → `other`).
- **Icon enum is identical in v0.9.1 and v1.0: 59 names** (not 60). v1.0
  additionally allows `{svgPath}` objects and dynamic bindings for `name`.
- **Capabilities metadata shapes** (A2A extension spec): renderer sends
  `message.metadata["a2uiRendererCapabilities"] = {"v1.0":
  {"supportedCatalogIds": [...]}}`; the v0.9-era shape is
  `metadata["a2uiClientCapabilities"] = {"v0.9": {...}}`. This engine sends
  both. `a2uiRendererDataModel = {"version":"v1.0","surfaces":{id:model}}`
  is attached only for surfaces created with `sendDataModel:true`.
- **A2UI detection** is `DataPart.data.metadata["mimeType"] ==
  "application/a2ui+json"`; `data` MUST be an array of envelopes (single
  object tolerated). Processing is explicitly non-transactional per the
  extension spec's "Processing Rules".
- **Catalog IDs are identifiers, not resolvable URLs** ("it does not need to
  point to any deployed resource"). The engine never fetches them.
- **v1.0 has no catalog fallback**: every component/function resolves via
  its own `catalogId` or the surface default. Since this engine only
  implements the basic catalog, components resolving elsewhere are treated
  as unknown (graceful placeholder), matching the spec's
  progressive-rendering mandate.

## Template scoping (pinned by tests)

Per the v1.0 spec's "Path resolution & scope": a `{componentId, path}`
ChildList template instantiates the referenced component per array element.
Inside the instantiated subtree, paths NOT starting with `/` resolve against
the element (Collection Scope); absolute paths always resolve against the
root data model. `@index` (with optional `offset` arg) is available only
inside template scope; outside it is an error. The scope propagates down
through nested id-references until another template overrides it. See
`surface_test.go` `TestTemplateScopeResolution`, which mirrors the spec's
worked example verbatim.

## Security posture

- openUrl is registered but gated: it validates http/https schemes and then
  returns `ErrGated`; the caller must obtain explicit user confirmation
  before actually opening anything.
- No URL in any component is ever fetched; media renders as placeholder
  lines.
- Size limits: 1 MiB per envelope, 64 MiB per envelope list, 10,000
  components per surface, data-model depth 32, materialized tree depth 64,
  50,000 materialized nodes, 1,000 template items per template, JSON
  Pointer ≤64 tokens. Cycles in the component graph render as placeholders.
- Regex uses Go's RE2 engine; patterns RE2 cannot compile (lookahead,
  backreferences) fail the check with no message rather than erroring the
  evaluation. A length/character budget guards hostile patterns.

## Known deviations

- `formatNumber` defaults decimals to 0 for integral values and 2 otherwise
  ("0 or 2 depending on locale" per the catalog; no locale negotiation
  exists in the engine).
- `formatCurrency` uses a small ISO-4217 symbol table and falls back to the
  code suffix (`1,500 CHF`); not the full CLDR symbol data.
- The static renderer shows only the first tab, renders modals inline
  (trigger + indented content), and renders inputs as value previews; the
  interactive widget layer lands in milestone M8.
