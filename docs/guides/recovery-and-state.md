---
page_title: "Recovery, competing writers, and state security"
description: |-
  Protect Terraform artifacts, recover an existing Cantora Agent Configuration through import, and understand the boundary between one state and other authorized Management API writers.
---

# Recovery, competing writers, and state security

Terraform state binds a `cantora_agent_configuration` address to one Agent Definition identifier. Cantora authorizes the calling ServicePrincipal, not a particular state lineage.

Use a remote backend that provides encryption, access control, locking, backup, and retention. Agent instructions and computed plan metadata enter saved plans and state. Credentials and Connection secret material must not enter Agent Configuration.

If state is lost or ownership moves to another root, import the existing Agent Definition with its complete `organization_id/project_id/agent_definition_id` identity. A create with an existing Project key is refused rather than adopted automatically.

Backend locking coordinates writers that share one state. Separate states and authorized Management API clients can change the same Agent Definition; refresh exposes the result as drift, and saved-plan ETags and generations reject stale apply attempts. Keep one Terraform binding for each remote object and use audit evidence to identify other writers.

Removing the resource from configuration removes only the Terraform binding. Cantora retains the Agent Definition, current runtime behavior, immutable Versions and Releases, Configuration Revisions, and audit evidence.
