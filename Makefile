default: vet vet-readme build test

.PHONY: build
build:
	go build ./...

.PHONY: test
test:
	go test ./...

.PHONY: vet
vet:
	go vet ./...

.PHONY: vet-readme
vet-readme:
	rm -f readme_test.go
	go tool mdextract -tags ci -output readme_test.go README.md
	go vet .
	rm -f readme_test.go
