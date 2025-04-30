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
		--arch amd64
