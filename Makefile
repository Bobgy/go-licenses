# Copyright 2022 Google Inc.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#      https://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# All of these should pass before sending a PR.
precommit: test lint tidy

test: FORCE
	go test ./...

lint:  FORCE
# Note, golangci-lint version is pinned in go.mod. When upgrading, also
# upgrade version in .github/workflows/golangci-lint.yml.
	go run github.com/golangci/golangci-lint/cmd/golangci-lint run -v

tidy: FORCE
	go mod tidy

VERSION=v2.0.0-beta.0

# Release on github.
# Note, edit the VERSION variable first.
release: update-main rename push-main FORCE
	gh release create $(VERSION) \
		--generate-notes \
		--notes-file release_template.md \
		--prerelease

update-main: FORCE
	git branch -D main || echo "main branch does not exist"
	git checkout -b main

push-main: FORCE
	git commit -a -m 'Version $(VERSION)'
	git poh -f

rename: FORCE
	find . -type f \
		-name '*.go' \
		-exec sed -i -e 's,github.com/google/go-licenses,github.com/Bobgy/go-licenses/v2,g' {} \;
	sed -i -e 's,github.com/google/go-licenses,github.com/Bobgy/go-licenses/v2,g' go.mod

FORCE: ;
