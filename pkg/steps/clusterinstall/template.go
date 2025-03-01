package clusterinstall

const installTemplateE2E = `
kind: Template
apiVersion: template.openshift.io/v1

parameters:
- name: JOB_NAME_SAFE
  required: true
- name: JOB_NAME_HASH
  required: true
- name: NAMESPACE
  required: true
- name: IMAGE_FORMAT
- name: IMAGE_INSTALLER
  required: true
- name: IMAGE_TESTS
  required: true
- name: CLUSTER_TYPE
  required: true
- name: TEST_COMMAND
  required: true
- name: RELEASE_IMAGE_LATEST
  required: true
- name: BASE_DOMAIN
- name: CLUSTER_NETWORK_MANIFEST
- name: CLUSTER_NETWORK_TYPE
- name: BUILD_ID
  required: false
- name: CLUSTER_VARIANT

objects:

# We want the cluster to be able to access these images
- kind: RoleBinding
  apiVersion: authorization.openshift.io/v1
  metadata:
    name: ${JOB_NAME_SAFE}-image-puller
    namespace: ${NAMESPACE}
  roleRef:
    name: system:image-puller
  subjects:
  - kind: SystemGroup
    name: system:unauthenticated
  - kind: SystemGroup
    name: system:authenticated

# Give admin access to a known bot
- kind: RoleBinding
  apiVersion: authorization.openshift.io/v1
  metadata:
    name: ${JOB_NAME_SAFE}-namespace-admins
    namespace: ${NAMESPACE}
  roleRef:
    name: admin
  subjects:
  - kind: ServiceAccount
    namespace: ci
    name: ci-chat-bot

# Role for giving the e2e pod permissions to update imagestreams
- kind: Role
  apiVersion: authorization.openshift.io/v1
  metadata:
    name: ${JOB_NAME_SAFE}-imagestream-updater
    namespace: ${NAMESPACE}
  rules:
  - apiGroups: ["image.openshift.io"]
    resources: ["imagestreams/layers"]
    verbs: ["get", "update"]
  - apiGroups: ["image.openshift.io"]
    resources: ["imagestreams", "imagestreamtags"]
    verbs: ["get", "create", "update", "delete", "list"]

# Give the e2e pod access to the imagestream-updater role
- kind: RoleBinding
  apiVersion: authorization.openshift.io/v1
  metadata:
    name: ${JOB_NAME_SAFE}-imagestream-updater-binding
    namespace: ${NAMESPACE}
  roleRef:
    kind: Role
    namespace: ${NAMESPACE}
    name: ${JOB_NAME_SAFE}-imagestream-updater
  subjects:
  - kind: ServiceAccount
    namespace: ${NAMESPACE}
    name: default

# The e2e pod spins up a cluster, runs e2e tests, and then cleans up the cluster.
- kind: Pod
  apiVersion: v1
  metadata:
    name: ${JOB_NAME_SAFE}
    namespace: ${NAMESPACE}
    annotations:
      # we want to gather the teardown logs no matter what
      ci-operator.openshift.io/wait-for-container-artifacts: teardown
      ci-operator.openshift.io/save-container-logs: "true"
      ci-operator.openshift.io/container-sub-tests: "lease,setup,test,teardown"
  spec:
    restartPolicy: Never
    activeDeadlineSeconds: 18000
    terminationGracePeriodSeconds: 900
    volumes:
    - name: artifacts
      emptyDir: {}
    - name: shared-tmp
      emptyDir: {}
    - name: cluster-profile
      secret:
        secretName: ${JOB_NAME_SAFE}-cluster-profile

    containers:

    - name: lease
      image: registry.svc.ci.openshift.org/ci/boskoscli:latest
      terminationMessagePolicy: FallbackToLogsOnError
      resources:
        requests:
          cpu: 10m
          memory: 10Mi
        limits:
          memory: 200Mi
      volumeMounts:
      - name: shared-tmp
        mountPath: /tmp/shared
      - name: cluster-profile
        mountPath: /tmp/cluster
      - name: artifacts
        mountPath: /tmp/artifacts
      env:
      - name: CLUSTER_TYPE
        value: ${CLUSTER_TYPE}
      - name: CLUSTER_NAME
        value: ${NAMESPACE}-${JOB_NAME_HASH}
      command:
      - /bin/bash
      - -c
      - |
        #!/bin/bash
        set -euo pipefail

        trap 'rc=$?; CHILDREN=$(jobs -p); if test -n "${CHILDREN}"; then kill ${CHILDREN} && wait; fi; if test "${rc}" -ne 0; then touch /tmp/shared/exit; fi; exit "${rc}"' EXIT

        # hack for bazel
        function boskosctl() {
          /app/boskos/cmd/cli/app.binary "${@}"
        }

        echo "[INFO] Acquiring a lease ..."
        resource="$( boskosctl --server-url http://boskos.ci --owner-name "${CLUSTER_NAME}" acquire --type "${CLUSTER_TYPE}-quota-slice" --state free --target-state leased --timeout 150m )"
        touch /tmp/shared/leased
        echo "[INFO] Lease acquired!"
        echo "[INFO] Leased resource: ${resource}"

        function release() {
            local resource_name; resource_name="$( jq .name --raw-output <<<"${resource}" )"
            echo "[INFO] Releasing the lease on resouce ${resource_name}..."
            boskosctl --server-url http://boskos.ci --owner-name "${CLUSTER_NAME}" release --name "${resource_name}" --target-state free
        }
        trap release EXIT

        echo "[INFO] Sending heartbeats to retain the lease..."
        boskosctl --server-url http://boskos.ci --owner-name "${CLUSTER_NAME}" heartbeat --resource "${resource}" &

        while true; do
          if [[ -f /tmp/shared/exit ]]; then
            echo "Another process exited" 2>&1
            exit 0
          fi

          sleep 15 & wait $!
        done

    # Once the cluster is up, executes shared tests
    - name: test
      image: ${IMAGE_TESTS}
      terminationMessagePolicy: FallbackToLogsOnError
      resources:
        requests:
          cpu: 3
          memory: 600Mi
        limits:
          memory: 4Gi
      volumeMounts:
      - name: shared-tmp
        mountPath: /tmp/shared
      - name: cluster-profile
        mountPath: /tmp/cluster
      - name: artifacts
        mountPath: /tmp/artifacts
      env:
      - name: AWS_SHARED_CREDENTIALS_FILE
        value: /tmp/cluster/.awscred
      - name: AZURE_AUTH_LOCATION
        value: /tmp/cluster/osServicePrincipal.json
      - name: GCP_SHARED_CREDENTIALS_FILE
        value: /tmp/cluster/gce.json
      - name: ARTIFACT_DIR
        value: /tmp/artifacts
      - name: HOME
        value: /tmp/home
      - name: IMAGE_FORMAT
        value: ${IMAGE_FORMAT}
      - name: KUBECONFIG
        value: /tmp/artifacts/installer/auth/kubeconfig
      - name: MIRROR_BASE
        value: registry.svc.ci.openshift.org/${NAMESPACE}/release
      command:
      - /bin/bash
      - -c
      - |
        #!/bin/bash
        set -euo pipefail

        export PATH=/usr/libexec/origin:$PATH

        trap 'touch /tmp/shared/exit' EXIT
        trap 'kill $(jobs -p); exit 0' TERM

        function fips_check() {
          oc --insecure-skip-tls-verify --request-timeout=60s get nodes -o jsonpath --template '{range .items[*]}{.metadata.name}{"\n"}{end}' > /tmp/nodelist
          while IFS= read -r i; do
            oc -n default --insecure-skip-tls-verify --request-timeout=60s debug node/$i -- cat /proc/sys/crypto/fips_enabled > /tmp/enabled
            if [[ "${CLUSTER_VARIANT}" =~ "fips" ]]; then
              if [[ $(< /tmp/enabled) == "0" ]]; then
                echo fips not enabled in node "$i" but should be, exiting
                exit 1
              fi
            else
              if [[ $(< /tmp/enabled) == "1" ]]; then
                echo fips is enabled in node "$i" but should not be, exiting
                exit 1
              fi
            fi
          done </tmp/nodelist
          rm -f /tmp/nodelist
          rm -f /tmp/enabled
        }

        function patch_image_specs() {
          cat <<EOF >samples-patch.yaml
        - op: add
          path: /spec/skippedImagestreams
          value:
          - jenkins
          - jenkins-agent-maven
          - jenkins-agent-nodejs
        EOF
          oc patch config.samples.operator.openshift.io cluster --type json -p "$(cat samples-patch.yaml)"

          NAMES='cli cli-artifacts installer installer-artifacts must-gather tests jenkins jenkins-agent-maven jenkins-agent-nodejs'
          cat <<EOF >version-patch.yaml
        - op: add
          path: /spec/overrides
          value:
        EOF
          for NAME in ${NAMES}
          do
            cat <<EOF >>version-patch.yaml
          - group: image.openshift.io/v1
            kind: ImageStream
            name: ${NAME}
            namespace: openshift
            unmanaged: true
        EOF
          done
          oc patch clusterversion version --type json -p "$(cat version-patch.yaml)"

          for NAME in ${NAMES}
          do
            DIGEST="$(oc adm release info --image-for="${NAME}" | sed 's/.*@//')"
            cat <<EOF >image-stream-new-source.yaml
        - op: replace
          path: /spec/tags/0/from
          value:
            kind: DockerImage
            name: "${MIRROR_BASE}@${DIGEST}"
        EOF
            oc -n openshift patch imagestream "${NAME}" --type json -p "$(cat image-stream-new-source.yaml)"
          done
        }

        mkdir -p "${HOME}"

        # Share oc with other containers
        cp "$(command -v oc)" /tmp/shared

        # wait for the API to come up
        while true; do
          if [[ -f /tmp/shared/exit ]]; then
            echo "Another process exited" 2>&1
            exit 1
          fi
          if [[ ! -f /tmp/shared/setup-success ]]; then
            sleep 15 & wait
            continue
          fi
          # don't let clients impact the global kubeconfig
          cp "${KUBECONFIG}" /tmp/admin.kubeconfig
          export KUBECONFIG=/tmp/admin.kubeconfig
          break
        done

        # if the cluster profile included an insights secret, install it to the cluster to
        # report support data from the support-operator
        if [[ -f /tmp/cluster/insights-live.yaml ]]; then
          oc create -f /tmp/cluster/insights-live.yaml || true
        fi

        # set up cloud-provider-specific env vars
        export KUBE_SSH_BASTION="$( oc --insecure-skip-tls-verify get node -l node-role.kubernetes.io/master -o 'jsonpath={.items[0].status.addresses[?(@.type=="ExternalIP")].address}' ):22"
        export KUBE_SSH_KEY_PATH=/tmp/cluster/ssh-privatekey
        if [[ "${CLUSTER_TYPE}" == "gcp" ]]; then
          export GOOGLE_APPLICATION_CREDENTIALS="${GCP_SHARED_CREDENTIALS_FILE}"
          export KUBE_SSH_USER=core
          mkdir -p ~/.ssh
          cp /tmp/cluster/ssh-privatekey ~/.ssh/google_compute_engine || true
          export TEST_PROVIDER='{"type":"gce","region":"us-east1","multizone": true,"multimaster":true,"projectid":"openshift-gce-devel-ci"}'
        elif [[ "${CLUSTER_TYPE}" == "aws" ]]; then
          mkdir -p ~/.ssh
          cp /tmp/cluster/ssh-privatekey ~/.ssh/kube_aws_rsa || true
          export PROVIDER_ARGS="-provider=aws -gce-zone=us-east-1"
          # TODO: make openshift-tests auto-discover this from cluster config
          REGION="$(oc get -o jsonpath='{.status.platformStatus.aws.region}' infrastructure cluster)"
          ZONE="$(oc get -o jsonpath='{.items[0].metadata.labels.failure-domain\.beta\.kubernetes\.io/zone}' nodes)"
          export TEST_PROVIDER="{\"type\":\"aws\",\"region\":\"${REGION}\",\"zone\":\"${ZONE}\",\"multizone\":true,\"multimaster\":true}"
          export KUBE_SSH_USER=core
        elif [[ "${CLUSTER_TYPE}" == "azure4" ]]; then
          export TEST_PROVIDER='azure'
        fi

        mkdir -p /tmp/output
        cd /tmp/output

        function setup_ssh_bastion() {
          export SSH_BASTION_NAMESPACE=test-ssh-bastion
          echo "Setting up ssh bastion"
          mkdir -p ~/.ssh
          cp "${KUBE_SSH_KEY_PATH}" ~/.ssh/id_rsa
          chmod 0600 ~/.ssh/id_rsa
          if ! whoami &> /dev/null; then
            if [[ -w /etc/passwd ]]; then
              echo "${USER_NAME:-default}:x:$(id -u):0:${USER_NAME:-default} user:${HOME}:/sbin/nologin" >> /etc/passwd
            fi
          fi
          curl https://raw.githubusercontent.com/eparis/ssh-bastion/master/deploy/deploy.sh | bash
          for i in $(seq 0 30); do
            # AWS fills only .hostname of a service
            BASTION_HOST=$(oc get service -n "${SSH_BASTION_NAMESPACE}" ssh-bastion -o jsonpath='{.status.loadBalancer.ingress[0].hostname}')
            if [[ -n "${BASTION_HOST}" ]]; then break; fi
            # Azure fills only .ip of a service. Use it as bastion host.
            BASTION_HOST=$(oc get service -n "${SSH_BASTION_NAMESPACE}" ssh-bastion -o jsonpath='{.status.loadBalancer.ingress[0].ip}')
            if [[ -n "${BASTION_HOST}" ]]; then break; fi
            echo "Waiting for SSH bastion load balancer service"
            sleep 10
          done
          if [[ -z "${BASTION_HOST}" ]]; then
            echo "Failed to find bastion address, exiting"
            exit 1
          fi
          export KUBE_SSH_BASTION="${BASTION_HOST}:22"
        }

        function setup-google-cloud-sdk() {
          pushd /tmp
          curl -O https://dl.google.com/dl/cloudsdk/channels/rapid/downloads/google-cloud-sdk-256.0.0-linux-x86_64.tar.gz
          tar -xzf google-cloud-sdk-256.0.0-linux-x86_64.tar.gz
          export PATH=$PATH:/tmp/google-cloud-sdk/bin
          mkdir gcloudconfig
          export CLOUDSDK_CONFIG=/tmp/gcloudconfig
          gcloud auth activate-service-account --key-file="${GCP_SHARED_CREDENTIALS_FILE}"
          gcloud config set project openshift-gce-devel-ci
          popd
        }

        function run-upgrade-tests() {
          openshift-tests run-upgrade "${TEST_SUITE}" --to-image "${RELEASE_IMAGE_LATEST}" \
            --provider "${TEST_PROVIDER:-}" -o /tmp/artifacts/e2e.log --junit-dir /tmp/artifacts/junit
        }

        function run-tests() {
          openshift-tests run "${TEST_SUITE}" \
            --provider "${TEST_PROVIDER:-}" -o /tmp/artifacts/e2e.log --junit-dir /tmp/artifacts/junit
        }

        if [[ "${CLUSTER_TYPE}" == "gcp" ]]; then
          setup-google-cloud-sdk
        fi

        ${TEST_COMMAND}

    # Runs an install
    - name: setup
      image: ${IMAGE_INSTALLER}
      terminationMessagePolicy: FallbackToLogsOnError
      volumeMounts:
      - name: shared-tmp
        mountPath: /tmp
      - name: cluster-profile
        mountPath: /etc/openshift-installer
      - name: artifacts
        mountPath: /tmp/artifacts
      env:
      - name: AWS_SHARED_CREDENTIALS_FILE
        value: /etc/openshift-installer/.awscred
      - name: AWS_REGION
        value: us-east-1
      - name: AZURE_AUTH_LOCATION
        value: /etc/openshift-installer/osServicePrincipal.json
      - name: GCP_REGION
        value: us-east1
      - name: GCP_PROJECT
        value: openshift-gce-devel-ci
      - name: GOOGLE_CLOUD_KEYFILE_JSON
        value: /etc/openshift-installer/gce.json
      - name: CLUSTER_NAME
        value: ${NAMESPACE}-${JOB_NAME_HASH}
      - name: CLUSTER_VARIANT
        value: ${CLUSTER_VARIANT}
      - name: BASE_DOMAIN
        value: ${BASE_DOMAIN}
      - name: SSH_PRIV_KEY_PATH
        value: /etc/openshift-installer/ssh-privatekey
      - name: SSH_PUB_KEY_PATH
        value: /etc/openshift-installer/ssh-publickey
      - name: PULL_SECRET_PATH
        value: /etc/openshift-installer/pull-secret
      - name: OPENSHIFT_INSTALL_RELEASE_IMAGE_OVERRIDE
        value: ${RELEASE_IMAGE_LATEST}
      - name: OPENSHIFT_INSTALL_INVOKER
        value: openshift-internal-ci/${JOB_NAME_SAFE}/${BUILD_ID}
      - name: USER
        value: test
      - name: HOME
        value: /tmp
      - name: MIRROR_BASE
        value: registry.svc.ci.openshift.org/${NAMESPACE}/release
      - name: INSTALL_INITIAL_RELEASE
      - name: RELEASE_IMAGE_INITIAL
      command:
      - /bin/bash
      - -c
      - |
        #!/bin/sh
        set -e

        trap 'rc=$?; if test "${rc}" -eq 0; then touch /tmp/setup-success; else touch /tmp/exit; fi; exit "${rc}"' EXIT
        trap 'CHILDREN=$(jobs -p); if test -n "${CHILDREN}"; then kill ${CHILDREN} && wait; fi' TERM

        while true; do
          if [[ -f /tmp/exit ]]; then
            echo "Another process exited" 2>&1
            exit 1
          fi
          if [[ -f /tmp/leased ]]; then
            echo "Lease acquired, installing..."
            break
          fi

          sleep 15 & wait
        done

        cp "$(command -v openshift-install)" /tmp
        mkdir /tmp/artifacts/installer

        if [[ -n "${INSTALL_INITIAL_RELEASE}" && -n "${RELEASE_IMAGE_INITIAL}" ]]; then
          echo "Installing from initial release ${RELEASE_IMAGE_INITIAL}"
          OPENSHIFT_INSTALL_RELEASE_IMAGE_OVERRIDE="${RELEASE_IMAGE_INITIAL}"
        elif [[ "${CLUSTER_VARIANT}" =~ "mirror" ]]; then
          export PATH=$PATH:/tmp  # gain access to oc
          while [ ! command -V oc ]; do sleep 1; done # poll to make sure that the test container has dropped oc into the shared volume

          # mirror the release image and override the release image to point to the mirrored one
          mkdir /tmp/.docker && cp /etc/openshift-installer/pull-secret /tmp/.docker/config.json
          oc registry login
          oc adm release new --from-release ${RELEASE_IMAGE_LATEST} --to-image ${MIRROR_BASE}-scratch:release --mirror ${MIRROR_BASE}-scratch || echo 'ignore: the release could not be reproduced from its inputs'
          oc adm release mirror --from ${MIRROR_BASE}-scratch:release --to ${MIRROR_BASE} --to-release-image ${MIRROR_BASE}:mirrored
          RELEASE_PAYLOAD_IMAGE_SHA=$(oc get istag ${MIRROR_BASE##*/}:mirrored -o=jsonpath="{.image.metadata.name}")
          oc delete imagestream "$(basename "${MIRROR_BASE}-scratch")"
          RELEASE_IMAGE_MIRROR="${MIRROR_BASE}@${RELEASE_PAYLOAD_IMAGE_SHA}"

          echo "Installing from mirror override release ${RELEASE_IMAGE_MIRROR}"
          OPENSHIFT_INSTALL_RELEASE_IMAGE_OVERRIDE="${RELEASE_IMAGE_MIRROR}"
        else
          echo "Installing from release ${RELEASE_IMAGE_LATEST}"
        fi

        export EXPIRATION_DATE=$(date -d '4 hours' --iso=minutes --utc)
        export SSH_PUB_KEY=$(cat "${SSH_PUB_KEY_PATH}")
        export PULL_SECRET=$(cat "${PULL_SECRET_PATH}")

        ## move private key to ~/.ssh/ so that installer can use it to gather logs on bootstrap failure
        mkdir -p ~/.ssh
        cp "${SSH_PRIV_KEY_PATH}" ~/.ssh/

        workers=3
        if [[ "${CLUSTER_VARIANT}" =~ "compact" ]]; then
          workers=0
        fi
        if [[ "${CLUSTER_TYPE}" = "aws" ]]; then
            subnets="[]"
            if [[ "${CLUSTER_VARIANT}" =~ "shared-vpc" ]]; then
              case $((RANDOM % 4)) in
              0) subnets="['subnet-030a88e6e97101ab2','subnet-0e07763243186cac5','subnet-02c5fea7482f804fb','subnet-0291499fd1718ee01','subnet-01c4667ad446c8337','subnet-025e9043c44114baa']";;
              1) subnets="['subnet-0170ee5ccdd7e7823','subnet-0d50cac95bebb5a6e','subnet-0094864467fc2e737','subnet-0daa3919d85296eb6','subnet-0ab1e11d3ed63cc97','subnet-07681ad7ce2b6c281']";;
              2) subnets="['subnet-00de9462cf29cd3d3','subnet-06595d2851257b4df','subnet-04bbfdd9ca1b67e74','subnet-096992ef7d807f6b4','subnet-0b3d7ba41fc6278b2','subnet-0b99293450e2edb13']";;
              3) subnets="['subnet-047f6294332aa3c1c','subnet-0c3bce80bbc2c8f1c','subnet-038c38c7d96364d7f','subnet-027a025e9d9db95ce','subnet-04d9008469025b101','subnet-02f75024b00b20a75']";;
              *) echo >&2 "invalid subnets index"; exit 1;;
              esac
              echo "Subnets : ${subnets}"
            fi
            cat > /tmp/artifacts/installer/install-config.yaml << EOF
        apiVersion: v1
        baseDomain: ${BASE_DOMAIN:-origin-ci-int-aws.dev.rhcloud.com}
        metadata:
          name: ${CLUSTER_NAME}
        controlPlane:
          name: master
          replicas: 3
          platform:
            aws:
              zones:
              - us-east-1a
              - us-east-1b
        compute:
        - name: worker
          replicas: ${workers}
          platform:
            aws:
              type: m4.xlarge
              zones:
              - us-east-1a
              - us-east-1b
        platform:
          aws:
            region:       ${AWS_REGION}
            userTags:
              expirationDate: ${EXPIRATION_DATE}
            subnets: ${subnets}
        pullSecret: >
          ${PULL_SECRET}
        sshKey: |
          ${SSH_PUB_KEY}
        EOF

        elif [[ "${CLUSTER_TYPE}" == "azure4" ]]; then
            case $((RANDOM % 4)) in
            0) AZURE_REGION=centralus;;
            1) AZURE_REGION=eastus;;
            2) AZURE_REGION=eastus2;;
            3) AZURE_REGION=westus;;
            *) echo >&2 "invalid Azure region index"; exit 1;;
            esac
            echo "Azure region: ${AZURE_REGION}"

            vnetrg=""
            vnetname=""
            ctrlsubnet=""
            computesubnet=""
            if [[ "${CLUSTER_VARIANT}" =~ "shared-vpc" ]]; then
              vnetrg="os4-common"
              vnetname="do-not-delete-shared-vnet-${AZURE_REGION}"
              ctrlsubnet="subnet-1"
              computesubnet="subnet-2"
            fi
            cat > /tmp/artifacts/installer/install-config.yaml << EOF
        apiVersion: v1
        baseDomain: ${BASE_DOMAIN:-ci.azure.devcluster.openshift.com}
        metadata:
          name: ${CLUSTER_NAME}
        controlPlane:
          name: master
          replicas: 3
        compute:
        - name: worker
          replicas: ${workers}
        platform:
          azure:
            baseDomainResourceGroupName: os4-common
            region: ${AZURE_REGION}
            networkResourceGroupName: ${vnetrg}
            virtualNetwork: ${vnetname}
            controlPlaneSubnet: ${ctrlsubnet}
            computeSubnet: ${computesubnet}
        pullSecret: >
          ${PULL_SECRET}
        sshKey: |
          ${SSH_PUB_KEY}
        EOF
        elif [[ "${CLUSTER_TYPE}" == "gcp" ]]; then
            # HACK: try to "poke" the token endpoint before the test starts
            for i in $(seq 1 30); do
              code="$( curl -s -o /dev/null -w "%{http_code}" https://oauth2.googleapis.com/token -X POST -d '' )"
              if [[ "${code}" == "400" ]]; then
                break
              fi
              echo "error: Unable to resolve https://oauth2.googleapis.com/token: $code" 1>&2
              if [[ "${i}" == "30" ]]; then
                echo "error: Unable to resolve https://oauth2.googleapis.com/token within timeout, exiting" 1>&2
                exit 1
              fi
              sleep 1
            done
            network=""
            ctrlsubnet=""
            computesubnet=""
            if [[ "${CLUSTER_VARIANT}" =~ "shared-vpc" ]]; then
              network="do-not-delete-shared-network"
              ctrlsubnet="do-not-delete-shared-master-subnet"
              computesubnet="do-not-delete-shared-worker-subnet"
            fi
            cat > /tmp/artifacts/installer/install-config.yaml << EOF
        apiVersion: v1
        baseDomain: ${BASE_DOMAIN:-origin-ci-int-gce.dev.openshift.com}
        metadata:
          name: ${CLUSTER_NAME}
        controlPlane:
          name: master
          replicas: 3
        compute:
        - name: worker
          replicas: ${workers}
        platform:
          gcp:
            projectID: ${GCP_PROJECT}
            region: ${GCP_REGION}
            network: ${network}
            controlPlaneSubnet: ${ctrlsubnet}
            computeSubnet: ${computesubnet}
        pullSecret: >
          ${PULL_SECRET}
        sshKey: |
          ${SSH_PUB_KEY}
        EOF
        else
            echo "Unsupported cluster type '${CLUSTER_TYPE}'"
            exit 1
        fi

        # as a current stop gap -- this is pointing to a proxy hosted in
        # the namespace "ci-test-ewolinet" on the ci cluster
        if [[ "${CLUSTER_VARIANT}" =~ "proxy" ]]; then

        # FIXME: due to https://bugzilla.redhat.com/show_bug.cgi?id=1750650 we need to
        # use a http endpoint for the httpsProxy value
        # TODO: revert back to using https://ewolinet:5f6ccbbbafc66013d012839921ada773@35.231.5.161:3128/

          cat >> /tmp/artifacts/installer/install-config.yaml << EOF
        proxy:
          httpsProxy: http://ewolinet:5f6ccbbbafc66013d012839921ada773@35.196.128.173:3128/
          httpProxy: http://ewolinet:5f6ccbbbafc66013d012839921ada773@35.196.128.173:3128/
        additionalTrustBundle: |
          -----BEGIN CERTIFICATE-----
          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
          GtklzYOidGtxo1MT
          -----END CERTIFICATE-----
          -----BEGIN CERTIFICATE-----
          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
          Q/6M
          -----END CERTIFICATE-----
        EOF
        fi

        network_type="${CLUSTER_NETWORK_TYPE-}"
        if [[ "${CLUSTER_VARIANT}" =~ "ovn" ]]; then
          network_type=OVNKubernetes
        fi
        if [[ -n "${network_type}" ]]; then
          cat >> /tmp/artifacts/installer/install-config.yaml << EOF
        networking:
          networkType: ${network_type}
        EOF
        fi

        if [[ "${CLUSTER_VARIANT}" =~ "mirror" ]]; then
          cat >> /tmp/artifacts/installer/install-config.yaml << EOF
        imageContentSources:
        - source: "${MIRROR_BASE}-scratch"
          mirrors:
          - "${MIRROR_BASE}"
        EOF
        fi

        if [[ "${CLUSTER_VARIANT}" =~ "fips" ]]; then
          cat >> /tmp/artifacts/installer/install-config.yaml << EOF
        fips: true
        EOF
        fi

        # TODO: Replace with a more concise manifest injection approach
        if [[ -n "${CLUSTER_NETWORK_MANIFEST}" ]]; then
            openshift-install --dir=/tmp/artifacts/installer/ create manifests
            echo "${CLUSTER_NETWORK_MANIFEST}" > /tmp/artifacts/installer/manifests/cluster-network-03-config.yml
        fi

        TF_LOG=debug openshift-install --dir=/tmp/artifacts/installer create cluster &
        wait "$!"

    # Performs cleanup of all created resources
    - name: teardown
      image: ${IMAGE_TESTS}
      terminationMessagePolicy: FallbackToLogsOnError
      volumeMounts:
      - name: shared-tmp
        mountPath: /tmp/shared
      - name: cluster-profile
        mountPath: /etc/openshift-installer
      - name: artifacts
        mountPath: /tmp/artifacts
      env:
      - name: INSTANCE_PREFIX
        value: ${NAMESPACE}-${JOB_NAME_HASH}
      - name: AWS_SHARED_CREDENTIALS_FILE
        value: /etc/openshift-installer/.awscred
      - name: AZURE_AUTH_LOCATION
        value: /etc/openshift-installer/osServicePrincipal.json
      - name: GOOGLE_CLOUD_KEYFILE_JSON
        value: /etc/openshift-installer/gce.json
      - name: KUBECONFIG
        value: /tmp/artifacts/installer/auth/kubeconfig
      command:
      - /bin/bash
      - -c
      - |
        #!/bin/bash
        function queue() {
          local TARGET="${1}"
          shift
          local LIVE="$(jobs | wc -l)"
          while [[ "${LIVE}" -ge 45 ]]; do
            sleep 1
            LIVE="$(jobs | wc -l)"
          done
          echo "${@}"
          if [[ -n "${FILTER}" ]]; then
            "${@}" | "${FILTER}" >"${TARGET}" &
          else
            "${@}" >"${TARGET}" &
          fi
        }

        function teardown() {
          set +e
          touch /tmp/shared/exit
          export PATH=$PATH:/tmp/shared

          echo "Gathering artifacts ..."
          mkdir -p /tmp/artifacts/pods /tmp/artifacts/nodes /tmp/artifacts/metrics /tmp/artifacts/bootstrap /tmp/artifacts/network


          if [ -f /tmp/artifacts/installer/terraform.tfstate ]
          then
              # we don't have jq, so the python equivalent of
              # jq '.modules[].resources."aws_instance.bootstrap".primary.attributes."public_ip" | select(.)'
              bootstrap_ip=$(python -c \
                  'import sys, json; d=reduce(lambda x,y: dict(x.items() + y.items()), map(lambda x: x["resources"], json.load(sys.stdin)["modules"])); k="aws_instance.bootstrap"; print d[k]["primary"]["attributes"]["public_ip"] if k in d else ""' \
                  < /tmp/artifacts/installer/terraform.tfstate
              )

              if [ -n "${bootstrap_ip}" ]
              then
                for service in bootkube openshift kubelet crio
                do
                    queue "/tmp/artifacts/bootstrap/${service}.service" curl \
                        --insecure \
                        --silent \
                        --connect-timeout 5 \
                        --retry 3 \
                        --cert /tmp/artifacts/installer/tls/journal-gatewayd.crt \
                        --key /tmp/artifacts/installer/tls/journal-gatewayd.key \
                        --url "https://${bootstrap_ip}:19531/entries?_SYSTEMD_UNIT=${service}.service"
                done
                if ! whoami &> /dev/null; then
                  if [ -w /etc/passwd ]; then
                    echo "${USER_NAME:-default}:x:$(id -u):0:${USER_NAME:-default} user:${HOME}:/sbin/nologin" >> /etc/passwd
                  fi
                fi
                eval $(ssh-agent)
                ssh-add /etc/openshift-installer/ssh-privatekey
                ssh -A -o PreferredAuthentications=publickey -o StrictHostKeyChecking=false -o UserKnownHostsFile=/dev/null core@${bootstrap_ip} /bin/bash -x /usr/local/bin/installer-gather.sh
                scp -o PreferredAuthentications=publickey -o StrictHostKeyChecking=false -o UserKnownHostsFile=/dev/null core@${bootstrap_ip}:log-bundle.tar.gz /tmp/artifacts/installer/bootstrap-logs.tar.gz
              fi
          else
              echo "No terraform statefile found. Skipping collection of bootstrap logs."
          fi

          oc --insecure-skip-tls-verify --request-timeout=5s get nodes -o jsonpath --template '{range .items[*]}{.metadata.name}{"\n"}{end}' > /tmp/nodes
          oc --insecure-skip-tls-verify --request-timeout=5s get pods --all-namespaces --template '{{ range .items }}{{ $name := .metadata.name }}{{ $ns := .metadata.namespace }}{{ range .spec.containers }}-n {{ $ns }} {{ $name }} -c {{ .name }}{{ "\n" }}{{ end }}{{ range .spec.initContainers }}-n {{ $ns }} {{ $name }} -c {{ .name }}{{ "\n" }}{{ end }}{{ end }}' > /tmp/containers
          oc --insecure-skip-tls-verify --request-timeout=5s get pods -l openshift.io/component=api --all-namespaces --template '{{ range .items }}-n {{ .metadata.namespace }} {{ .metadata.name }}{{ "\n" }}{{ end }}' > /tmp/pods-api

          queue /tmp/artifacts/config-resources.json oc --insecure-skip-tls-verify --request-timeout=5s get apiserver.config.openshift.io authentication.config.openshift.io build.config.openshift.io console.config.openshift.io dns.config.openshift.io featuregate.config.openshift.io image.config.openshift.io infrastructure.config.openshift.io ingress.config.openshift.io network.config.openshift.io oauth.config.openshift.io project.config.openshift.io scheduler.config.openshift.io -o json
          queue /tmp/artifacts/apiservices.json oc --insecure-skip-tls-verify --request-timeout=5s get apiservices -o json
          queue /tmp/artifacts/clusteroperators.json oc --insecure-skip-tls-verify --request-timeout=5s get clusteroperators -o json
          queue /tmp/artifacts/clusterversion.json oc --insecure-skip-tls-verify --request-timeout=5s get clusterversion -o json
          queue /tmp/artifacts/configmaps.json oc --insecure-skip-tls-verify --request-timeout=5s get configmaps --all-namespaces -o json
          queue /tmp/artifacts/credentialsrequests.json oc --insecure-skip-tls-verify --request-timeout=5s get credentialsrequests --all-namespaces -o json
          queue /tmp/artifacts/csr.json oc --insecure-skip-tls-verify --request-timeout=5s get csr -o json
          queue /tmp/artifacts/endpoints.json oc --insecure-skip-tls-verify --request-timeout=5s get endpoints --all-namespaces -o json
          FILTER=gzip queue /tmp/artifacts/deployments.json.gz oc --insecure-skip-tls-verify --request-timeout=5s get deployments --all-namespaces -o json
          FILTER=gzip queue /tmp/artifacts/daemonsets.json.gz oc --insecure-skip-tls-verify --request-timeout=5s get daemonsets --all-namespaces -o json
          queue /tmp/artifacts/events.json oc --insecure-skip-tls-verify --request-timeout=5s get events --all-namespaces -o json
          queue /tmp/artifacts/kubeapiserver.json oc --insecure-skip-tls-verify --request-timeout=5s get kubeapiserver -o json
          queue /tmp/artifacts/kubecontrollermanager.json oc --insecure-skip-tls-verify --request-timeout=5s get kubecontrollermanager -o json
          queue /tmp/artifacts/machineconfigpools.json oc --insecure-skip-tls-verify --request-timeout=5s get machineconfigpools -o json
          queue /tmp/artifacts/machineconfigs.json oc --insecure-skip-tls-verify --request-timeout=5s get machineconfigs -o json
          queue /tmp/artifacts/machinesets.json oc --insecure-skip-tls-verify --request-timeout=5s get machinesets -A -o json
          queue /tmp/artifacts/machines.json oc --insecure-skip-tls-verify --request-timeout=5s get machines -A -o json
          queue /tmp/artifacts/namespaces.json oc --insecure-skip-tls-verify --request-timeout=5s get namespaces -o json
          queue /tmp/artifacts/nodes.json oc --insecure-skip-tls-verify --request-timeout=5s get nodes -o json
          queue /tmp/artifacts/openshiftapiserver.json oc --insecure-skip-tls-verify --request-timeout=5s get openshiftapiserver -o json
          queue /tmp/artifacts/pods.json oc --insecure-skip-tls-verify --request-timeout=5s get pods --all-namespaces -o json
          queue /tmp/artifacts/persistentvolumes.json oc --insecure-skip-tls-verify --request-timeout=5s get persistentvolumes --all-namespaces -o json
          queue /tmp/artifacts/persistentvolumeclaims.json oc --insecure-skip-tls-verify --request-timeout=5s get persistentvolumeclaims --all-namespaces -o json
          FILTER=gzip queue /tmp/artifacts/replicasets.json.gz oc --insecure-skip-tls-verify --request-timeout=5s get replicasets --all-namespaces -o json
          queue /tmp/artifacts/rolebindings.json oc --insecure-skip-tls-verify --request-timeout=5s get rolebindings --all-namespaces -o json
          queue /tmp/artifacts/roles.json oc --insecure-skip-tls-verify --request-timeout=5s get roles --all-namespaces -o json
          queue /tmp/artifacts/services.json oc --insecure-skip-tls-verify --request-timeout=5s get services --all-namespaces -o json
          FILTER=gzip queue /tmp/artifacts/statefulsets.json.gz oc --insecure-skip-tls-verify --request-timeout=5s get statefulsets --all-namespaces -o json

          FILTER=gzip queue /tmp/artifacts/openapi.json.gz oc --insecure-skip-tls-verify --request-timeout=5s get --raw /openapi/v2

          # gather nodes first in parallel since they may contain the most relevant debugging info
          while IFS= read -r i; do
            mkdir -p /tmp/artifacts/nodes/$i
            queue /tmp/artifacts/nodes/$i/heap oc --insecure-skip-tls-verify get --request-timeout=20s --raw /api/v1/nodes/$i/proxy/debug/pprof/heap
          done < /tmp/nodes

          FILTER=gzip queue /tmp/artifacts/nodes/masters-journal.gz oc --insecure-skip-tls-verify adm node-logs --role=master --unify=false
          FILTER=gzip queue /tmp/artifacts/nodes/workers-journal.gz oc --insecure-skip-tls-verify adm node-logs --role=worker --unify=false

          # Snapshot iptables-save on each node for debugging possible kube-proxy issues
          oc --insecure-skip-tls-verify get --request-timeout=20s -n openshift-sdn -l app=sdn pods --template '{{ range .items }}{{ .metadata.name }}{{ "\n" }}{{ end }}' > /tmp/sdn-pods
          while IFS= read -r i; do
            queue /tmp/artifacts/network/iptables-save-$i oc --insecure-skip-tls-verify rsh --timeout=20 -n openshift-sdn -c sdn $i iptables-save -c
          done < /tmp/sdn-pods

          while IFS= read -r i; do
            file="$( echo "$i" | cut -d ' ' -f 3 | tr -s ' ' '_' )"
            queue /tmp/artifacts/metrics/${file}-heap oc --insecure-skip-tls-verify exec $i -- /bin/bash -c 'oc --insecure-skip-tls-verify get --raw /debug/pprof/heap --server "https://$( hostname ):8443" --config /etc/origin/master/admin.kubeconfig'
            queue /tmp/artifacts/metrics/${file}-controllers-heap oc --insecure-skip-tls-verify exec $i -- /bin/bash -c 'oc --insecure-skip-tls-verify get --raw /debug/pprof/heap --server "https://$( hostname ):8444" --config /etc/origin/master/admin.kubeconfig'
          done < /tmp/pods-api

          while IFS= read -r i; do
            file="$( echo "$i" | cut -d ' ' -f 2,3,5 | tr -s ' ' '_' )"
            FILTER=gzip queue /tmp/artifacts/pods/${file}.log.gz oc --insecure-skip-tls-verify logs --request-timeout=20s $i
            FILTER=gzip queue /tmp/artifacts/pods/${file}_previous.log.gz oc --insecure-skip-tls-verify logs --request-timeout=20s -p $i
          done < /tmp/containers

          echo "Snapshotting prometheus (may take 15s) ..."
          queue /tmp/artifacts/metrics/prometheus.tar.gz oc --insecure-skip-tls-verify exec -n openshift-monitoring prometheus-k8s-0 -- tar cvzf - -C /prometheus .

          echo "Running must-gather..."
          mkdir -p /tmp/artifacts/must-gather
          queue /tmp/artifacts/must-gather/must-gather.log oc --insecure-skip-tls-verify adm must-gather --dest-dir /tmp/artifacts/must-gather

          echo "Waiting for logs ..."
          wait

          echo "Deprovisioning cluster ..."
          openshift-install --dir /tmp/artifacts/installer destroy cluster
        }

        trap 'teardown' EXIT
        trap 'kill $(jobs -p); exit 0' TERM

        for i in $(seq 1 220); do
          if [[ -f /tmp/shared/exit ]]; then
            exit 0
          fi
          sleep 60 & wait
        done
`
