keygen:
	@rm -rf testdata/packages
	@mkdir -p testdata/packages
	@cd testdata/packages && melange keygen

test-packages: keygen
	@echo "Building test packages"
	melange build -i testdata/charts/melange.yaml \
		--signing-key testdata/packages/melange.rsa \
		--keyring-append testdata/packages/melange.rsa.pub \
		--out-dir testdata/packages \
		--source-dir testdata/charts \
		--arch amd64 \
		--generate-index=false
	melange build -i testdata/charts/melange-versioned-bumped.yaml \
		--signing-key testdata/packages/melange.rsa \
		--keyring-append testdata/packages/melange.rsa.pub \
		--out-dir testdata/packages \
		--source-dir testdata/charts \
		--arch amd64 \
		--generate-index=false
	cd testdata/packages/x86_64 && melange index \
		--signing-key ../melange.rsa \
		-o APKINDEX.tar.gz \
		$$(ls *.apk)
