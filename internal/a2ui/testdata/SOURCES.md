# Vendored A2UI specification fixtures

These files are copied verbatim from the a2ui-project/a2ui GitHub repository
(upstream license: Apache License 2.0, see the license text at
https://github.com/a2ui-project/a2ui/blob/main/LICENSE) and are used as
golden inputs for envelope decoding, engine application and rendering tests.

Upstream files (main branch, retrieved 2026-09-11):

| File in testdata/ | Upstream path |
| --- | --- |
| contact_form_example.jsonl | specification/v1_0/test/cases/contact_form_example.jsonl |
| example_01_flight_status.json | specification/v1_0/catalogs/basic/examples/01_flight-status.json |
| example_09_login_form.json | specification/v1_0/catalogs/basic/examples/09_login-form.json |
| example_34_child_list_template.json | specification/v1_0/catalogs/basic/examples/34_child-list-template.json |
| example_36_modal.json | specification/v1_0/catalogs/basic/examples/36_modal.json |

Canonical URLs use the pattern:

- https://raw.githubusercontent.com/a2ui-project/a2ui/main/specification/v1_0/test/cases/contact_form_example.jsonl
- https://raw.githubusercontent.com/a2ui-project/a2ui/main/specification/v1_0/catalogs/basic/examples/<name>.json

The `.json` examples each carry `{"name", "description", "messages": [...]}`
where `messages` is a list of A2UI v1.0 envelopes. The `.jsonl` file is one
envelope per line (the spec's Contact Form worked example).

Upstream copyright: The A2UI project authors. Licensed under the Apache
License, Version 2.0: http://www.apache.org/licenses/LICENSE-2.0
