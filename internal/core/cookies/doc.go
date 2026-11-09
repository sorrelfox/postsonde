// Package cookies is PostSonde's cookie jar (CK-01): an RFC 6265 cookie
// store, with the rfc6265bis updates browsers implement, that the app can
// list and persist.
//
// net/http/cookiejar implements the same matching rules but can neither
// list nor persist its cookies, and it only sees cookies already parsed by
// net/http, which drops some that browsers accept. This package parses the
// raw Set-Cookie field values itself (RFC 6265 §5.2, leniently, like a
// browser) and applies the storage model (§5.3) and the retrieval rules
// (§5.4):
//
//   - [Jar] holds the cookies. [Jar.SetCookieLines] stores the Set-Cookie
//     values of a response, [Jar.Cookies] returns the cookies to send to a
//     URL in Cookie header order. Jar also implements http.CookieJar, so
//     other net/http clients can use it.
//   - [Open] loads a jar from a workspace's local directory
//     (<local>/cookies.json) and saves it there after changes; a
//     [Registry] keeps one per local directory.
//
// docs/cookies.md explains the decisions: the public suffix heuristic,
// limits, session cookies, Secure and SameSite for an API tool.
package cookies
