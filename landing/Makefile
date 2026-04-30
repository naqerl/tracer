.PHONY: clean dev-server

clean:
	rm -rf ./html

dev-server:
	@echo "Serving on http://localhost:$$PORT"
	python3 -m http.server $(PORT) --directory html
