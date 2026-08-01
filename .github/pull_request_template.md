<!--
Thanks for the pull request. Please fill this out — reviewers use it to
understand what changed and how it was verified, and it saves a round trip
of follow-up questions.
-->

## What does this change do, and why?

<!--
Describe the change and the motivation behind it. If it fixes a bug,
describe the bug. If it's a new feature, link the discussion or issue where
it was proposed, if any.
-->

## Related issue(s)

<!-- e.g. Closes #123, or "None" -->

## Testing

<!--
Describe what you ran and what passed. If a change genuinely can't be
tested (e.g. a documentation-only change), say so instead of leaving this
blank.
-->

- [ ] `go test ./...` passes locally (or the equivalent for the language of
      this repository)
- [ ] I added or updated tests covering this change, or explained above why
      that isn't applicable
- [ ] I manually verified the change (describe how, above)

## Checklist

- [ ] Every commit is signed off (`git commit -s`) per the
      [Developer Certificate of Origin](../CONTRIBUTING.md#developer-certificate-of-origin-dco)
- [ ] I did **not** run `go mod tidy` as part of this change (see
      [CONTRIBUTING.md](../CONTRIBUTING.md#building-this-repository))
- [ ] This PR touches a single logical change (unrelated cleanups are split
      out separately)
- [ ] If this change is part of a cross-repository feature, I've linked the
      companion pull request(s) in the other NimoOS repositories above
