# gofslock
Go implementation of filesystem-level locking.

[![GoDoc](https://godoc.org/github.com/danjacques/gofslock?status.svg)](http://godoc.org/github.com/danjacques/gofslock)
[![Build Status](https://travis-ci.org/danjacques/gofslock.svg?branch=master)](https://travis-ci.org/danjacques/gofslock)
[![Coverage Status](https://coveralls.io/repos/github/danjacques/gofslock/badge.svg?branch=master)](https://coveralls.io/github/danjacques/gofslock?branch=master)

`gofslock` offers several features:
* Exclusive and shared locking senamtics.
* Consistent intra- and inter-processing locking behavior across major operating
  systems (notably Linux, Mac, and Windows).
* Works on all Go versions.
* Only depends on Go standard library.
* Locking behavior and interaction is heavily tested.

Feedback
--------

Request features and report bugs using the
[GitHub Issue Tracker](https://github.com/danjacques/gofslock/issues/new).

Contributions
-------------

Contributions to this project are welcome, though please
[file an issue](https://github.com/danjacques/gofslock/issues/new)
before starting work on anything major.

To get started contributing to this project,
clone the repository:

    git clone https://github.com/danjacques/gofslock

This repository uses [pre-commit-go](https://github.com/maruel/pre-commit-go) to
validate itself. Please install this prior to working on the project:

  * Make sure your `user.email` and `user.name` are configured in `git config`.
  * Install test-only packages:
    `go get -u -t github.com/danjacques/gofslock/...`
  * Install the [pcg](https://github.com/maruel/pre-commit-go) git hook:
    `go get -u github.com/maruel/pre-commit-go/cmd/... && pcg`
