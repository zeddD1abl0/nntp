// Copyright 2009 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package nntp

import (
	"bufio"
	"bytes"
	"compress/zlib"
	"fmt"
	"io"
	"net/textproto"
	"strings"
	"testing"
	"time"

	log "github.com/Sirupsen/logrus"
)

func init() {
	log.SetLevel(log.DebugLevel)
}

func TestSanityChecks(t *testing.T) {
	if _, err := New("", ""); err == nil {
		t.Fatal("Dial should require at least a destination address.")
	}
}

type faker struct {
	io.Writer
	io.Reader
}

func (f faker) Close() error {
	return nil
}

func TestBasics(t *testing.T) {
	basicServer := makeBasicServer()
	basicClient = strings.Join(strings.Split(basicClient, "\n"), "\r\n")

	var cmdbuf bytes.Buffer
	var fake faker
	fake.Writer = &cmdbuf
	fake.Reader = bufio.NewReader(bytes.NewReader(basicServer))

	conn := &Conn{conn: textproto.NewConn(fake)}

	// Test some global commands that don't take arguments
	if _, err := conn.Capabilities(); err != nil {
		t.Fatal("should be able to request CAPABILITIES after connecting: " + err.Error())
	}

	_, err := conn.Date()
	if err != nil {
		t.Fatal("should be able to send DATE: " + err.Error())
	}

	/*
		 Test broken until time.Parse adds this format.
		cdate := time.UTC()
		if sdate.Year != cdate.Year || sdate.Month != cdate.Month || sdate.Day != cdate.Day {
			t.Fatal("DATE seems off, probably erroneous: " + sdate.String())
		}
	*/

	// Test LIST (implicit ACTIVE)
	if _, err = conn.List(); err != nil {
		t.Fatal("LIST should work: " + err.Error())
	}

	tt := time.Date(2010, time.March, 01, 00, 0, 0, 0, time.UTC)

	const groupName = "gmane.comp.lang.go.general"
	grp, err := conn.Group(groupName)
	if err != nil {
		t.Fatal("Group shouldn't error: " + err.Error())
	}
	if grp.Count != 1000 {
		t.Fatalf("Group's count not set correctly: %d vs %d", 1000, grp.Count)
	}
	if grp.Low != 500 {
		t.Fatalf("Group's low article number not set correctly: %d vs %d", 500, grp.Low)
	}
	if grp.High != 1000 {
		t.Fatalf("Group's high article number set correctly: %d vs %d", 1000, grp.High)
	}
	if grp.Name != groupName {
		t.Fatalf("Group's name not set correctly: %s vs %s", groupName, grp.Name)
	}

	// test STAT, NEXT, and LAST
	if _, _, err = conn.Stat(""); err != nil {
		t.Fatal("should be able to STAT after selecting a group: " + err.Error())
	}
	if _, _, err = conn.Next(); err != nil {
		t.Fatal("should be able to NEXT after selecting a group: " + err.Error())
	}
	if _, _, err = conn.Last(); err != nil {
		t.Fatal("should be able to LAST after a NEXT selecting a group: " + err.Error())
	}

	// Can we grab articles?
	a, err := conn.Article(fmt.Sprintf("%d", grp.Low))
	if err != nil {
		t.Fatal("should be able to fetch the low article: " + err.Error())
	}

	// Test that the article body doesn't get mangled.
	expectedbody := `Blah, blah.
.A single leading .
Fin.`
	body := strings.Join(a.Body, "\n")
	if expectedbody != body {
		t.Fatalf("article body read incorrectly; got:\n%s\nExpected:\n%s", body, expectedbody)
	}

	// Test articleReader
	expectedart := `Message-Id: <b@c.d>

Body.`
	a, err = conn.Article(fmt.Sprintf("%d", grp.Low+1))
	if err != nil {
		t.Fatal("shouldn't error reading article low+1: " + err.Error())
	}
	actualart := a.String()
	if actualart != expectedart {
		t.Fatalf("articleReader broke; got:\n%s\nExpected\n%s", actualart, expectedart)
	}

	// Just headers?
	if _, err = conn.Head(fmt.Sprintf("%d", grp.High)); err != nil {
		t.Fatal("should be able to fetch the high article: " + err.Error())
	}

	// Without an id?
	if _, err = conn.Head(""); err != nil {
		t.Fatal("should be able to fetch the selected article without specifying an id: " + err.Error())
	}

	// How about bad articles? Do they error?
	if _, err = conn.Head(fmt.Sprintf("%d", grp.Low-1)); err == nil {
		t.Fatal("shouldn't be able to fetch articles lower than low")
	}
	if _, err = conn.Head(fmt.Sprintf("%d", grp.High+1)); err == nil {
		t.Fatal("shouldn't be able to fetch articles higher than high")
	}

	// Just the body?
	_, err = conn.Body(fmt.Sprintf("%d", grp.Low))
	if err != nil {
		t.Fatal("should be able to fetch the low article body" + err.Error())
	}

	if _, err = conn.NewNews(groupName, tt); err != nil {
		t.Fatal("newnews should work: " + err.Error())
	}

	// NewGroups
	grps, err := conn.NewGroups(tt)
	if err != nil {
		t.Fatal("newgroups shouldn't error " + err.Error())
	}
	if len(grps) != 0 {
		t.Fatal("newgroups should return empty list when there are no new groups")
	}
	grps, err = conn.NewGroups(tt)
	if err != nil {
		t.Fatal("newgroups shouldn't error " + err.Error())
	}
	if len(grps) != 2 {
		t.Fatal("newgroups expected to return 2 groups")
	}

	// Overview
	overviews, err := conn.Overview(10, 11)
	if err != nil {
		t.Fatal("overview shouldn't error: " + err.Error())
	}
	expectedOverviews := []MessageOverview{
		MessageOverview{10, "Subject10", "Author <author@server>", time.Date(2003, 10, 18, 18, 0, 0, 0, time.FixedZone("", 1800)), "<d@e.f>", []string{}, 1000, 9, []string{}},
		MessageOverview{11, "Subject11", "", time.Date(2003, 10, 18, 19, 0, 0, 0, time.FixedZone("", 1800)), "<e@f.g>", []string{"<d@e.f>", "<a@b.c>"}, 2000, 18, []string{"Extra stuff"}},
	}

	if len(overviews) != len(expectedOverviews) {
		t.Fatalf("returned %d overviews, expected %d", len(overviews), len(expectedOverviews))
	}

	for i, o := range overviews {
		if fmt.Sprint(o) != fmt.Sprint(expectedOverviews[i]) {
			t.Fatalf("in place of %dth overview expected %v, got %v", i, expectedOverviews[i], o)
		}
	}

	//SetCompression
	err = conn.SetCompression()
	if err != nil {
		t.Fatal("SetCompression shouldn't error: " + err.Error())
	}

	// Overview with compression
	overviews, err = conn.Overview(10, 11)
	if err != nil {
		t.Fatal("overview shouldn't error: " + err.Error())
	}
	expectedOverviews = []MessageOverview{
		MessageOverview{10, "Subject10", "Author <author@server>", time.Date(2003, 10, 18, 18, 0, 0, 0, time.FixedZone("", 1800)), "<d@e.f>", []string{}, 1000, 9, []string{}},
		MessageOverview{11, "Subject11", "", time.Date(2003, 10, 18, 19, 0, 0, 0, time.FixedZone("", 1800)), "<e@f.g>", []string{"<d@e.f>", "<a@b.c>"}, 2000, 18, []string{"Extra stuff"}},
	}

	if len(overviews) != len(expectedOverviews) {
		t.Fatalf("returned %d overviews, expected %d", len(overviews), len(expectedOverviews))
	}

	for i, o := range overviews {
		if fmt.Sprint(o) != fmt.Sprint(expectedOverviews[i]) {
			t.Fatalf("in place of %dth overview expected %v, got %v", i, expectedOverviews[i], o)
		}
	}

	if err = conn.Quit(); err != nil {
		t.Fatal("Quit shouldn't error: " + err.Error())
	}

	actualcmds := cmdbuf.String()
	if basicClient != actualcmds {
		t.Fatalf("Got:\n%s\nExpected\n%s", actualcmds, basicClient)
	}
}

func makeBasicServer() []byte {
	var b bytes.Buffer
	w := zlib.NewWriter(&b)

	w.Write([]byte("10	Subject10	Author <author@server>	Sat, 18 Oct 2003 18:00:00 +0030	<d@e.f>		1000	9\r\n"))
	w.Write([]byte("11	Subject11		18 Oct 2003 19:00:00 +0030	<e@f.g>	<d@e.f> <a@b.c>	2000	18	Extra stuff\r\n"))
	w.Flush()
	w.Close()

	basicServer := strings.Join(strings.Split(`101 Capability list:
VERSION 2
.
111 20100329034158
215 Blah blah
foo 7 3 y
bar 000008 02 m
.
211 1000 500 1000 gmane.comp.lang.go.general
223 1 <a@b.c> status
223 2 <b@c.d> Article retrieved
223 1 <a@b.c> Article retrieved
220 1 <a@b.c> article
Path: fake!not-for-mail
From: Someone
Newsgroups: gmane.comp.lang.go.general
Subject: [go-nuts] What about base members?
Message-ID: <a@b.c>

Blah, blah.
..A single leading .
Fin.
.
220 2 <b@c.d> article
Message-ID: <b@c.d>

Body.
.
221 100 <c@d.e> head
Path: fake!not-for-mail
Message-ID: <c@d.e>
.
221 100 <c@d.e> head
Path: fake!not-for-mail
Message-ID: <c@d.e>
.
423 Bad article number
423 Bad article number
222 1 <a@b.c> body
Blah, blah.
..A single leading .
Fin.
.
230 list of new articles by message-id follows
<d@e.c>
.
231 New newsgroups follow
.
231 New newsgroups follow
alt.rfc-writers.recovery 4 1 y
tx.natives.recovery 89 56 y
.
224 Overview information for 10-11 follows1
10	Subject10	Author <author@server>	Sat, 18 Oct 2003 18:00:00 +0030	<d@e.f>		1000	9
11	Subject11		18 Oct 2003 19:00:00 +0030	<e@f.g>	<d@e.f> <a@b.c>	2000	18	Extra stuff
.
290 Feature enabled
224 xover information follows [COMPRESS=GZIP]
`, "\n"), "\r\n")

	fin := ".\r\n205 Bye!\r\n"

	sbytes := []byte(basicServer)
	sbytes = append(sbytes, b.Bytes()...)
	sbytes = append(sbytes, []byte(fin)...)
	return sbytes

}

var basicClient = `CAPABILITIES
DATE
LIST
GROUP gmane.comp.lang.go.general
STAT
NEXT
LAST
ARTICLE 500
ARTICLE 501
HEAD 1000
HEAD
HEAD 499
HEAD 1001
BODY 500
NEWNEWS gmane.comp.lang.go.general 20100301 000000 GMT
NEWGROUPS 20100301 000000 GMT
NEWGROUPS 20100301 000000 GMT
XOVER 10-11
XFEATURE COMPRESS GZIP
XOVER 10-11
QUIT
`
