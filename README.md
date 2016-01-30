nntp.go
=======

An NNTP (news) Client package for go (golang). Forked from [nntp](http://chrisfarms/nntp).
- Changed to using net/textproto
- Added support for compressed XOVER responses


Example
-------

```go
  // connect to news server
  conn, err := nntp.NewTLS("tcp", "news.example.com:563", nil)
  if err != nil {
    log.Fatalf("connection failed: %v", err)
  }

  // auth
  if err := conn.Authenticate("user", "pass"); err != nil {
    log.Fatalf("Could not authenticate")
  }

  // connect to a news group
  grpName := "alt.binaries.pictures"
  group, err := conn.Group(grpName)
  if err != nil {
    log.Fatalf("Could not connect to group %s: %v %d", grpName, err)
  }

  // fetch an article
  id := "<4c1c18ec$0$8490$c3e8da3@news.astraweb.com>"
  article, err := conn.Article(id)
  if err != nil {
    log.Fatalf("Could not fetch article %s: %v", id, err)
  }

  // read the article contents
  body := strings.Join(article.Body,"")
```
