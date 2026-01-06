.PHONY: deploy-panel deploy-collector deploy restart-panel restart-collector restart

# Load environment variables
include .env
export

deploy-panel:
	@echo "Deploying panel to $(TARGET_HOST)..."
	@cd .. && make build-release-panel
	@ssh $(TARGET_HOST) "systemctl stop tracer-panel"
	@scp ../bin/tracer-panel $(TARGET_HOST):/usr/local/bin/tracer-panel
	@ssh $(TARGET_HOST) "systemctl start tracer-panel"
	@echo "Panel deployed and restarted"

deploy-collector:
	@echo "Deploying collector to $(TARGET_HOST)..."
	@cd .. && make build-release-collector
	@ssh $(TARGET_HOST) "systemctl stop tracer-collector"
	@scp ../bin/tracer-collector $(TARGET_HOST):/usr/local/bin/tracer-collector
	@ssh $(TARGET_HOST) "systemctl start tracer-collector"
	@echo "Collector deployed and restarted"

deploy: deploy-panel deploy-collector
	@echo "All components deployed"

restart-panel:
	@ssh $(TARGET_HOST) "systemctl restart tracer-panel"
	@echo "Panel restarted"

restart-collector:
	@ssh $(TARGET_HOST) "systemctl restart tracer-collector"
	@echo "Collector restarted"

restart: restart-panel restart-collector
	@echo "All services restarted"
