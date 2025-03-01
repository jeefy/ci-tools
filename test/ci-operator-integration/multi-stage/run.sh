#!/bin/bash
set -euo pipefail

WORKDIR="$(mktemp -d)"
trap 'rm -rf "${WORKDIR}"' EXIT

TEST_ROOT="$(dirname "${BASH_SOURCE[0]}")"
readonly TEST_ROOT
readonly TEST_CONFIG_DIR="${TEST_ROOT}/configs"
readonly TEST_REGISTRY_DIR="${TEST_ROOT}/../../multistage-registry/registry"
readonly TEST_CONFIG="${TEST_CONFIG_DIR}/master/openshift-hyperkube-master.yaml"
readonly TEST_NAMESPACE="testns"
readonly EXPECTED1="${TEST_ROOT}/expected/hyperkube.json"
readonly EXPECTED2="${TEST_ROOT}/expected/installer.json"
readonly OUT="${WORKDIR}/out.json"
readonly ERR="${WORKDIR}/err.json"
readonly ARTIFACT_DIR="${WORKDIR}/artifacts"

export JOB_SPEC='{"type":"presubmit","job":"pull-ci-openshift-release-master-ci-operator-integration","buildid":"0","prowjobid":"uuid","refs":{"org":"openshift","repo":"ci-tools","base_ref":"master","base_sha":"af8a90a2faf965eeda949dc1c607c48d3ffcda3e","pulls":[{"number":1234,"author":"droslean","sha":"538680dfd2f6cff3b3506c80ca182dcb0dd22a58"}]}}'
# set by Prow
unset BUILD_ID

echo "[INFO] Running ci-operator in dry-mode..."
if ! ci-operator --dry-run --determinize-output --namespace "${TEST_NAMESPACE}" --config "${TEST_CONFIG}" 2> "${ERR}" | jq --sort-keys . > "${OUT}"; then
    echo "ERROR: ci-operator failed."
    cat "${ERR}"
    exit 1
fi

if ! diff "${EXPECTED1}" "${OUT}"; then
    echo "ERROR: differences have been found"
    exit 1
fi

# Run test with ci-operator-configresolver
ci-operator-configresolver -config "${TEST_CONFIG_DIR}" -registry "${TEST_REGISTRY_DIR}" -log-level debug -cycle 2m &
PID=$!
# Wait until ready
for (( i = 0; i < 10; i++ )); do
    if [[ "$(curl http://127.0.0.1:8081/healthz/ready 2>/dev/null)" == "OK" ]]; then
        break
    fi
    if [[ "${i}" -eq 10 ]]; then
        echo "[ERROR] Timed out waiting for ci-operator-configresolver to be ready"
        kill $PID
        wait $PID
        exit 1
    fi
    sleep 0.5
done

if ! ci-operator --dry-run --determinize-output --namespace "${TEST_NAMESPACE}" --config "${TEST_CONFIG}" \
    -resolver-address "http://127.0.0.1:8080" -org "openshift" -repo "installer" -branch "release-4.2" --lease-server "http://lease" 2> "${ERR}" | jq --sort-keys . > "${OUT}"; then
    echo "ERROR: ci-operator failed."
    cat "${ERR}"
    kill $PID
    wait $PID
    exit 1
fi

if grep "level=error" "${ERR}"; then
    echo "ERROR: ci-operator stderr contains error level messages"
    cat "${ERR}"
    kill $PID
    wait $PID
    exit 1
fi

if ! diff "${EXPECTED2}" "${OUT}"; then
    echo "ERROR: differences have been found"
    kill $PID
    wait $PID
    exit 1
fi

kill $PID
wait $PID

echo "[INFO] Success"
