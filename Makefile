# dagrun build and check targets.
#
# `make check` is the gate that runs before a commit: formatting, vet, the full
# test suite, and the template lint that keeps the shipped plans honest.

GO ?= go
TIMEOUT ?= 300s

.PHONY: test test-scale fuzz lint fmt templates check

## test: the full suite, everything under the module.
test:
	$(GO) test -timeout $(TIMEOUT) ./...

## test-scale: the store's scale tests, which are behind a build tag.
test-scale:
	$(GO) test -tags scale -timeout 600s ./internal/store/

## fuzz: a short parse fuzz run, enough to catch a parser regression locally.
fuzz:
	$(GO) test -fuzz FuzzParse -fuzztime 30s ./internal/plan/

## lint: vet only; it never rewrites anything.
lint:
	$(GO) vet ./...

## fmt: fails when any file is not gofmt-clean. It never rewrites: formatting is
## a decision for the author, and a check that edits files cannot be trusted as
## a gate.
fmt:
	@unformatted="$$(gofmt -l .)"; \
	if [ -n "$$unformatted" ]; then \
		echo "gofmt needed on:"; echo "$$unformatted"; exit 1; \
	fi

## templates: every shipped plan against both linters, verbosely, so advisory
## findings (allowed but printed) stay visible.
templates:
	$(GO) test ./internal/plan/ -run Template -v

## check: the full gate.
check: fmt lint test templates
