# Compliance assessment completion tool

`compliance` copies questionnaire answers from the most recent earlier completed assessment for each policy in an Azure DevOps compliance assessment group. It then saves each assessment, generates its work items, and completes child work items whose matching source activity was completed.

The command uses the Azure DevOps API directly. Authentication comes from `az login`, or from `AZURE_DEVOPS_EXT_PAT` when using a PAT configured for the Azure DevOps CLI.

Start with a read-only dry run:

```sh
go run ./cmd/compliance \
  -url https://dev.azure.com/ORGANIZATION/PROJECT/_compliance/product/PRODUCT-ID/assessments
```

The dry run selects the newest assessment group, identifies the latest completed source for each policy, checks current question IDs, and validates the prior work-item configuration. It does not save assessments or create work items.

After reviewing the output, pin the group and apply it:

```sh
go run ./cmd/compliance \
  -url https://dev.azure.com/ORGANIZATION/PROJECT/_compliance/product/PRODUCT-ID/assessments \
  -assessment-group ASSESSMENT-GROUP \
  -apply
```

Safety behavior:

- Completed assessments are skipped.
- Assessments whose last completion session failed are retried on the next apply run.
- Generated child work items are completed only when the same activity node was completed in the selected source assessment. Parent work items are left open, matching the source assessment behavior.
- In-progress assessments are skipped unless `-overwrite` is supplied.
- Prior answers are copied only when their question IDs still exist and their question definitions are unchanged in the current questionnaire.
- A changed questionnaire blocks that assessment when answers would be dropped. Use `-allow-partial` only after reviewing the dry-run output.
- `-answers-only` saves copied answers but does not generate work items or mark the assessment complete.
- `-source-group NAME` restricts all answers and work-item settings to one prior group.
- `-answers-file PATH` supplies complete answer overrides for assessments whose questionnaires changed. The file is a JSON object keyed by assessment name, with arrays of `{ "questionId": "...", "answers": ["..."] }` objects. Dry run rejects missing questions, unknown IDs, duplicate IDs, empty answers, and invalid option values.
- `-complete-activity ASSESSMENT=NODE-ID` explicitly approves completion of a current-only activity after review. It may be repeated, and dry run rejects IDs that are not present in the resulting assessment work.
- New work items use the current Azure Boards project root for area and iteration rather than historical child paths that may have been deleted.

The signed-in identity needs read and write access to compliance assessments and permission to create or update the configured Azure Boards work items.
