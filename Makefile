VERSION=v2.0.0-dev.0

# Release on github.
# Note, edit the VERSION variable first.
release: update-main rename push-main FORCE
	gh release create $(VERSION) \
		--notes-file release_template.md \
		--title "$(VERSION)" \
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

FORCE:
