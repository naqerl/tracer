.PHONY: build clean dev-server

build:
	@emacs -Q --batch --script build.el

clean:
	rm -rf html/ .packages/

dev-server:
	@echo "Serving on http://localhost:$$PORT"
	python3 -m http.server $(PORT) --directory html
