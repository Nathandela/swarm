#!/bin/sh
# Runs the Gradle DSL endpoint-contract matrix without building an APK or contacting a service.
set -eu

cd "$(dirname "$0")/.."
. ./toolchain.env
SWARM_PUSH_GATEWAY_URL=https://service-123456789012.europe-west1.run.app \
  ./gradlew --no-daemon --console=plain \
  :app:requireProductionPushConfig :app:verifyProductionPushOriginContract
