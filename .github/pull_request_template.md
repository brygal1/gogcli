## Summary

- Describe the change and why it exists.

## Testing

- [ ] `make test`
- [ ] `make lint`
- [ ] Manual smoke test if behavior depends on live Google APIs or local keychain state

## User-Facing Checks

- [ ] I noted any new flags, output changes, or behavior changes
- [ ] If this change touches contact-aware output, plain-text and JSON both preserve Google Contacts context and human-readable membership groups when available
- [ ] If this change touches contact enrichment, failures remain explicit rather than silently dropping context
