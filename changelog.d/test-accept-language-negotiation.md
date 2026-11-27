none

Internal only: `internal/i18n` gained a table test for
`Manager.DetectFromAcceptLanguage`, the function every HTML request uses to pick
a language when the owner's cookie is absent. It was covered by two assertions
only. The new test pins what the parser must not lose — each `Accept-Language`
token is examined in the order the browser sent it, its `;q=` parameter is
stripped before matching, and an unsupported or empty token is skipped rather
than ending the search — plus a second manager configured with a different
default, so the fallback is shown to be the operator's `DEFAULT_LANGUAGE` and
not a hard-coded `en`. No production code changed.