package load

import (
	"encoding/json"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"testing"

	"github.com/openshift/ci-tools/pkg/api"
	"k8s.io/apimachinery/pkg/util/diff"
)

const rawConfig = `tag_specification:
  name: '4.0'
  namespace: ocp
promotion:
  name: '4.0'
  namespace: ocp
  additional_images:
    artifacts: artifacts
  excluded_images:
  - machine-os-content
base_images:
  base:
    name: '4.0'
    namespace: ocp
    tag: base
  base-machine:
    cluster: https://api.ci.openshift.org
    name: fedora
    namespace: openshift
    tag: '29'
  machine-os-content-base:
    name: '4.0'
    namespace: ocp
    tag: machine-os-content
binary_build_commands: make build WHAT='cmd/hypershift vendor/k8s.io/kubernetes/cmd/hyperkube'
canonical_go_repository: github.com/openshift/origin
images:
- dockerfile_path: images/template-service-broker/Dockerfile.rhel
  from: base
  to: template-service-broker
  inputs:
    bin:
      as:
      - builder
- dockerfile_path: images/cli/Dockerfile.rhel
  from: base
  to: cli
  inputs:
    bin:
      as:
      - builder
- dockerfile_path: images/hypershift/Dockerfile.rhel
  from: base
  to: hypershift
  inputs:
    bin:
      as:
      - builder
- dockerfile_path: images/hyperkube/Dockerfile.rhel
  from: base
  to: hyperkube
  inputs:
    bin:
      as:
      - builder
- dockerfile_path: images/tests/Dockerfile.rhel
  from: cli
  to: tests
  inputs:
    bin:
      as:
      - builder
- context_dir: images/deployer/
  dockerfile_path: Dockerfile.rhel
  from: cli
  to: deployer
- context_dir: images/recycler/
  dockerfile_path: Dockerfile.rhel
  from: cli
  to: recycler
- dockerfile_path: images/sdn/Dockerfile.rhel
  from: base
  to: node # TODO: SDN
  inputs:
    bin:
      as:
      - builder
- context_dir: images/os/
  from: base
  inputs:
    base-machine-with-rpms:
      as:
      - builder
    machine-os-content-base:
      as:
      -  registry.svc.ci.openshift.org/openshift/origin-v4.0:machine-os-content
  to: machine-os-content
raw_steps:
- pipeline_image_cache_step:
    commands: mkdir -p _output/local/releases; touch _output/local/releases/CHECKSUM;
      echo $'FROM bin AS bin\nFROM rpms AS rpms\nFROM centos:7\nCOPY --from=bin /go/src/github.com/openshift/origin/_output/local/releases
      /srv/zips/\nCOPY --from=rpms /go/src/github.com/openshift/origin/_output/local/releases/rpms/*
      /srv/repo/' > _output/local/releases/Dockerfile; make build-cross
    from: bin
    to: bin-cross
- project_directory_image_build_step:
    from: base
    inputs:
      bin-cross:
        as:
        - bin
        paths:
        - destination_dir: .
          source_path: /go/src/github.com/openshift/origin/_output/local/releases/Dockerfile
      rpms:
        as:
        - rpms
      src: {}
    optional: true
    to: artifacts
- output_image_tag_step:
    from: artifacts
    optional: true
    to:
      name: stable
      tag: artifacts
- rpm_image_injection_step:
    from: base
    to: base-with-rpms
- rpm_image_injection_step:
    from: base-machine
    to: base-machine-with-rpms
resources:
  '*':
    limits:
      memory: 6Gi
    requests:
      cpu: 100m
      memory: 200Mi
  bin:
    limits:
      memory: 12Gi
    requests:
      cpu: '3'
      memory: 8Gi
  bin-cross:
    limits:
      memory: 12Gi
    requests:
      cpu: '3'
      memory: 8Gi
  cmd:
    limits:
      memory: 11Gi
    requests:
      cpu: '3'
      memory: 8Gi
  integration:
    limits:
      memory: 18Gi
    requests:
      cpu: '3'
      memory: 14Gi
  rpms:
    limits:
      memory: 10Gi
    requests:
      cpu: '3'
      memory: 8Gi
  unit:
    limits:
      memory: 14Gi
    requests:
      cpu: '3'
      memory: 11Gi
  verify:
    limits:
      memory: 12Gi
    requests:
      cpu: '3'
      memory: 8Gi
rpm_build_commands: make build-rpms
build_root:
  image_stream_tag:
    cluster: https://api.ci.openshift.org
    name: src-cache-origin
    namespace: ci
    tag: master
tests:
- artifact_dir: /tmp/artifacts
  as: cmd
  commands: TMPDIR=/tmp/volume ARTIFACT_DIR=/tmp/artifacts JUNIT_REPORT=1
    KUBERNETES_SERVICE_HOST= make test-cmd -k
  container:
    from: bin
    memory_backed_volume:
      size: 4Gi
- artifact_dir: /tmp/artifacts
  as: unit
  commands: ARTIFACT_DIR=/tmp/artifacts JUNIT_REPORT=1 TEST_KUBE=true KUBERNETES_SERVICE_HOST=
    hack/test-go.sh
  container:
    from: src
- artifact_dir: /tmp/artifacts
  as: integration
  commands: GOMAXPROCS=8 TMPDIR=/tmp/volume ARTIFACT_DIR=/tmp/artifacts JUNIT_REPORT=1
    KUBERNETES_SERVICE_HOST= make test-integration
  container:
    from: bin
    memory_backed_volume:
      size: 4Gi
- artifact_dir: /tmp/artifacts
  as: verify
  commands: ARTIFACT_DIR=/tmp/artifacts JUNIT_REPORT=1 KUBERNETES_SERVICE_HOST= make
    verify -k
  container:
    from: bin
- as: e2e-aws
  commands: TEST_SUITE=openshift/conformance/parallel run-tests
  openshift_installer:
    cluster_profile: aws
- as: e2e-aws-all
  commands: TEST_SUITE=openshift/conformance run-tests
  openshift_installer:
    cluster_profile: aws
- as: e2e-aws-builds
  commands: TEST_SUITE=openshift/build run-tests
  openshift_installer:
    cluster_profile: aws
- as: e2e-aws-image-ecosystem
  commands: TEST_SUITE=openshift/image-ecosystem run-tests
  openshift_installer:
    cluster_profile: aws
- as: e2e-aws-image-registry
  commands: TEST_SUITE=openshift/image-registry run-tests
  openshift_installer:
    cluster_profile: aws
- as: e2e-aws-serial
  commands: TEST_SUITE=openshift/conformance/serial run-tests
  openshift_installer:
    cluster_profile: aws
- as: e2e-conformance-k8s
  commands: test/extended/conformance-k8s.sh
  openshift_installer_src:
    cluster_profile: aws
- as: launch-aws
  commands: sleep 7200 & wait
  openshift_installer:
    cluster_profile: aws
- as: e2e-upi-aws
  commands: TEST_SUITE=openshift/conformance/serial run-tests
  openshift_installer_upi:
    cluster_profile: aws
- as: e2e-upi-src-vsphere
  commands: make tests
  openshift_installer_upi_src:
    cluster_profile: vsphere
`

func strP(str string) *string {
	return &str
}

var parsedConfig = &api.ReleaseBuildConfiguration{
	InputConfiguration: api.InputConfiguration{
		BaseImages: map[string]api.ImageStreamTagReference{
			"base": {
				Name:      "4.0",
				Namespace: "ocp",
				Tag:       "base",
			},
			"base-machine": {
				Cluster:   "https://api.ci.openshift.org",
				Name:      "fedora",
				Namespace: "openshift",
				Tag:       "29",
			},
			"machine-os-content-base": {
				Name:      "4.0",
				Namespace: "ocp",
				Tag:       "machine-os-content",
			},
		},
		BuildRootImage: &api.BuildRootImageConfiguration{
			ImageStreamTagReference: &api.ImageStreamTagReference{
				Cluster:   "https://api.ci.openshift.org",
				Name:      "src-cache-origin",
				Namespace: "ci",
				Tag:       "master",
			},
		},
		ReleaseTagConfiguration: &api.ReleaseTagConfiguration{
			Name:      "4.0",
			Namespace: "ocp",
		},
	},
	BinaryBuildCommands:     `make build WHAT='cmd/hypershift vendor/k8s.io/kubernetes/cmd/hyperkube'`,
	TestBinaryBuildCommands: "",
	RpmBuildCommands:        "make build-rpms",
	RpmBuildLocation:        "",
	CanonicalGoRepository:   strP("github.com/openshift/origin"),
	Images: []api.ProjectDirectoryImageBuildStepConfiguration{{
		From: "base",
		To:   "template-service-broker",
		ProjectDirectoryImageBuildInputs: api.ProjectDirectoryImageBuildInputs{
			DockerfilePath: "images/template-service-broker/Dockerfile.rhel",
			Inputs:         map[string]api.ImageBuildInputs{"bin": {As: []string{"builder"}}},
		},
	}, {
		From: "base",
		To:   "cli",
		ProjectDirectoryImageBuildInputs: api.ProjectDirectoryImageBuildInputs{
			DockerfilePath: "images/cli/Dockerfile.rhel",
			Inputs:         map[string]api.ImageBuildInputs{"bin": {As: []string{"builder"}}},
		},
	}, {
		From: "base",
		To:   "hypershift",
		ProjectDirectoryImageBuildInputs: api.ProjectDirectoryImageBuildInputs{
			DockerfilePath: "images/hypershift/Dockerfile.rhel",
			Inputs:         map[string]api.ImageBuildInputs{"bin": {As: []string{"builder"}}},
		},
	}, {
		From: "base",
		To:   "hyperkube",
		ProjectDirectoryImageBuildInputs: api.ProjectDirectoryImageBuildInputs{
			DockerfilePath: "images/hyperkube/Dockerfile.rhel",
			Inputs:         map[string]api.ImageBuildInputs{"bin": {As: []string{"builder"}}},
		},
	}, {
		From: "cli",
		To:   "tests",
		ProjectDirectoryImageBuildInputs: api.ProjectDirectoryImageBuildInputs{
			DockerfilePath: "images/tests/Dockerfile.rhel",
			Inputs:         map[string]api.ImageBuildInputs{"bin": {As: []string{"builder"}}},
		},
	}, {
		From: "cli",
		To:   "deployer",
		ProjectDirectoryImageBuildInputs: api.ProjectDirectoryImageBuildInputs{
			DockerfilePath: "Dockerfile.rhel",
			ContextDir:     "images/deployer/",
		},
	}, {
		From: "cli",
		To:   "recycler",
		ProjectDirectoryImageBuildInputs: api.ProjectDirectoryImageBuildInputs{
			DockerfilePath: "Dockerfile.rhel",
			ContextDir:     "images/recycler/",
		},
	}, {
		From: "base",
		To:   "node",
		ProjectDirectoryImageBuildInputs: api.ProjectDirectoryImageBuildInputs{
			DockerfilePath: "images/sdn/Dockerfile.rhel",
			Inputs:         map[string]api.ImageBuildInputs{"bin": {As: []string{"builder"}}},
		},
	}, {
		From: "base",
		To:   "machine-os-content",
		ProjectDirectoryImageBuildInputs: api.ProjectDirectoryImageBuildInputs{
			ContextDir: "images/os/",
			Inputs: map[string]api.ImageBuildInputs{
				"base-machine-with-rpms":  {As: []string{"builder"}},
				"machine-os-content-base": {As: []string{"registry.svc.ci.openshift.org/openshift/origin-v4.0:machine-os-content"}},
			},
		},
	}},
	RawSteps: []api.StepConfiguration{{
		PipelineImageCacheStepConfiguration: &api.PipelineImageCacheStepConfiguration{
			From:     "bin",
			To:       "bin-cross",
			Commands: `mkdir -p _output/local/releases; touch _output/local/releases/CHECKSUM; echo $'FROM bin AS bin\nFROM rpms AS rpms\nFROM centos:7\nCOPY --from=bin /go/src/github.com/openshift/origin/_output/local/releases /srv/zips/\nCOPY --from=rpms /go/src/github.com/openshift/origin/_output/local/releases/rpms/* /srv/repo/' > _output/local/releases/Dockerfile; make build-cross`,
		},
	}, {
		ProjectDirectoryImageBuildStepConfiguration: &api.ProjectDirectoryImageBuildStepConfiguration{
			From: "base",
			To:   "artifacts",
			ProjectDirectoryImageBuildInputs: api.ProjectDirectoryImageBuildInputs{
				Inputs: map[string]api.ImageBuildInputs{
					"bin-cross": {As: []string{"bin"}, Paths: []api.ImageSourcePath{{DestinationDir: ".", SourcePath: "/go/src/github.com/openshift/origin/_output/local/releases/Dockerfile"}}},
					"rpms":      {As: []string{"rpms"}},
					"src":       {},
				},
			},
			Optional: true,
		},
	}, {
		OutputImageTagStepConfiguration: &api.OutputImageTagStepConfiguration{
			From:     "artifacts",
			To:       api.ImageStreamTagReference{Name: "stable", Tag: "artifacts"},
			Optional: true,
		},
	}, {
		RPMImageInjectionStepConfiguration: &api.RPMImageInjectionStepConfiguration{
			From: "base",
			To:   "base-with-rpms",
		},
	}, {
		RPMImageInjectionStepConfiguration: &api.RPMImageInjectionStepConfiguration{
			From: "base-machine",
			To:   "base-machine-with-rpms",
		},
	}},
	PromotionConfiguration: &api.PromotionConfiguration{
		Namespace:        "ocp",
		Name:             "4.0",
		AdditionalImages: map[string]string{"artifacts": "artifacts"},
		ExcludedImages:   []string{"machine-os-content"},
	},
	Resources: map[string]api.ResourceRequirements{
		"*":           {Limits: map[string]string{"memory": "6Gi"}, Requests: map[string]string{"cpu": "100m", "memory": "200Mi"}},
		"bin":         {Limits: map[string]string{"memory": "12Gi"}, Requests: map[string]string{"cpu": "3", "memory": "8Gi"}},
		"bin-cross":   {Limits: map[string]string{"memory": "12Gi"}, Requests: map[string]string{"cpu": "3", "memory": "8Gi"}},
		"cmd":         {Limits: map[string]string{"memory": "11Gi"}, Requests: map[string]string{"cpu": "3", "memory": "8Gi"}},
		"integration": {Limits: map[string]string{"memory": "18Gi"}, Requests: map[string]string{"cpu": "3", "memory": "14Gi"}},
		"rpms":        {Limits: map[string]string{"memory": "10Gi"}, Requests: map[string]string{"cpu": "3", "memory": "8Gi"}},
		"unit":        {Limits: map[string]string{"memory": "14Gi"}, Requests: map[string]string{"cpu": "3", "memory": "11Gi"}},
		"verify":      {Limits: map[string]string{"memory": "12Gi"}, Requests: map[string]string{"cpu": "3", "memory": "8Gi"}},
	},
	Tests: []api.TestStepConfiguration{{
		As:          "cmd",
		ArtifactDir: "/tmp/artifacts",
		Commands:    `TMPDIR=/tmp/volume ARTIFACT_DIR=/tmp/artifacts JUNIT_REPORT=1 KUBERNETES_SERVICE_HOST= make test-cmd -k`,
		ContainerTestConfiguration: &api.ContainerTestConfiguration{
			From: "bin",
			MemoryBackedVolume: &api.MemoryBackedVolume{
				Size: "4Gi",
			},
		},
	}, {
		As:          "unit",
		ArtifactDir: "/tmp/artifacts",
		Commands:    `ARTIFACT_DIR=/tmp/artifacts JUNIT_REPORT=1 TEST_KUBE=true KUBERNETES_SERVICE_HOST= hack/test-go.sh`,
		ContainerTestConfiguration: &api.ContainerTestConfiguration{
			From: "src",
		},
	}, {
		As:          "integration",
		ArtifactDir: "/tmp/artifacts",
		Commands:    `GOMAXPROCS=8 TMPDIR=/tmp/volume ARTIFACT_DIR=/tmp/artifacts JUNIT_REPORT=1 KUBERNETES_SERVICE_HOST= make test-integration`,
		ContainerTestConfiguration: &api.ContainerTestConfiguration{
			From: "bin",
			MemoryBackedVolume: &api.MemoryBackedVolume{
				Size: "4Gi",
			},
		},
	}, {
		As:          "verify",
		ArtifactDir: "/tmp/artifacts",
		Commands:    `ARTIFACT_DIR=/tmp/artifacts JUNIT_REPORT=1 KUBERNETES_SERVICE_HOST= make verify -k`,
		ContainerTestConfiguration: &api.ContainerTestConfiguration{
			From: "bin",
		},
	}, {
		As:       "e2e-aws",
		Commands: `TEST_SUITE=openshift/conformance/parallel run-tests`,
		OpenshiftInstallerClusterTestConfiguration: &api.OpenshiftInstallerClusterTestConfiguration{
			ClusterTestConfiguration: api.ClusterTestConfiguration{ClusterProfile: "aws"},
		},
	}, {
		As:       "e2e-aws-all",
		Commands: `TEST_SUITE=openshift/conformance run-tests`,
		OpenshiftInstallerClusterTestConfiguration: &api.OpenshiftInstallerClusterTestConfiguration{
			ClusterTestConfiguration: api.ClusterTestConfiguration{ClusterProfile: "aws"},
		},
	}, {
		As:       "e2e-aws-builds",
		Commands: `TEST_SUITE=openshift/build run-tests`,
		OpenshiftInstallerClusterTestConfiguration: &api.OpenshiftInstallerClusterTestConfiguration{
			ClusterTestConfiguration: api.ClusterTestConfiguration{ClusterProfile: "aws"},
		},
	}, {
		As:       "e2e-aws-image-ecosystem",
		Commands: `TEST_SUITE=openshift/image-ecosystem run-tests`,
		OpenshiftInstallerClusterTestConfiguration: &api.OpenshiftInstallerClusterTestConfiguration{
			ClusterTestConfiguration: api.ClusterTestConfiguration{ClusterProfile: "aws"},
		},
	}, {
		As:       "e2e-aws-image-registry",
		Commands: `TEST_SUITE=openshift/image-registry run-tests`,
		OpenshiftInstallerClusterTestConfiguration: &api.OpenshiftInstallerClusterTestConfiguration{
			ClusterTestConfiguration: api.ClusterTestConfiguration{ClusterProfile: "aws"},
		},
	}, {
		As:       "e2e-aws-serial",
		Commands: `TEST_SUITE=openshift/conformance/serial run-tests`,
		OpenshiftInstallerClusterTestConfiguration: &api.OpenshiftInstallerClusterTestConfiguration{
			ClusterTestConfiguration: api.ClusterTestConfiguration{ClusterProfile: "aws"},
		},
	}, {
		As:       "e2e-conformance-k8s",
		Commands: `test/extended/conformance-k8s.sh`,
		OpenshiftInstallerSrcClusterTestConfiguration: &api.OpenshiftInstallerSrcClusterTestConfiguration{
			ClusterTestConfiguration: api.ClusterTestConfiguration{ClusterProfile: "aws"},
		},
	}, {
		As:       "launch-aws",
		Commands: `sleep 7200 & wait`,
		OpenshiftInstallerClusterTestConfiguration: &api.OpenshiftInstallerClusterTestConfiguration{
			ClusterTestConfiguration: api.ClusterTestConfiguration{ClusterProfile: "aws"},
		},
	}, {
		As:       "e2e-upi-aws",
		Commands: `TEST_SUITE=openshift/conformance/serial run-tests`,
		OpenshiftInstallerUPIClusterTestConfiguration: &api.OpenshiftInstallerUPIClusterTestConfiguration{
			ClusterTestConfiguration: api.ClusterTestConfiguration{ClusterProfile: "aws"},
		},
	}, {
		As:       "e2e-upi-src-vsphere",
		Commands: `make tests`,
		OpenshiftInstallerUPISrcClusterTestConfiguration: &api.OpenshiftInstallerUPISrcClusterTestConfiguration{
			ClusterTestConfiguration: api.ClusterTestConfiguration{ClusterProfile: "vsphere"},
		},
	}},
}

func TestConfig(t *testing.T) {
	var testCases = []struct {
		name          string
		asFile        bool
		asEnv         bool
		expected      *api.ReleaseBuildConfiguration
		expectedError bool
	}{
		{
			name:          "loading config from file works",
			asFile:        true,
			expected:      parsedConfig,
			expectedError: false,
		},
		{
			name:          "loading config from env works",
			asEnv:         true,
			expected:      parsedConfig,
			expectedError: false,
		},
		{
			name:          "no file or env fails to load config",
			asEnv:         true,
			expected:      parsedConfig,
			expectedError: false,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			var path string
			if testCase.asFile {
				temp, err := ioutil.TempFile("", "")
				if err != nil {
					t.Fatalf("%s: failed to create temp config file: %v", testCase.name, err)
				}
				defer func() {
					if err := os.Remove(temp.Name()); err != nil {
						t.Fatalf("%s: failed to remove temp config file: %v", testCase.name, err)
					}
				}()
				path = temp.Name()

				if err := ioutil.WriteFile(path, []byte(rawConfig), 0664); err != nil {
					t.Fatalf("%s: failed to populate temp config file: %v", testCase.name, err)
				}
			}
			if testCase.asEnv {
				if err := os.Setenv("CONFIG_SPEC", rawConfig); err != nil {
					t.Fatalf("%s: failed to populate env var: %v", testCase.name, err)
				}
			}
			config, err := Config(path, nil)
			if err == nil && testCase.expectedError {
				t.Errorf("%s: expected an error, but got none", testCase.name)
			}
			if err != nil && !testCase.expectedError {
				t.Errorf("%s: expected no error, but got one: %v", testCase.name, err)
			}
			if actual, expected := config, testCase.expected; !reflect.DeepEqual(actual, expected) {
				t.Errorf("%s: didn't get correct config: %v", testCase.name, diff.ObjectReflectDiff(actual, expected))
			}

		})
	}
}

func TestConfigFromResolver(t *testing.T) {
	correctHandler := func(t *testing.T, jsonConfig []byte) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Query().Get("org") != "openshift" {
				t.Errorf("%s: Org should equal openshift, but was %s", t.Name(), r.URL.Query().Get("org"))
			}
			if r.URL.Query().Get("repo") != "hyperkube" {
				t.Errorf("%s: Repo should equal hyperkube, but was %s", t.Name(), r.URL.Query().Get("repo"))
			}
			if r.URL.Query().Get("branch") != "master" {
				t.Errorf("%s: Branch should equal master, but was %s", t.Name(), r.URL.Query().Get("branch"))
			}
			w.WriteHeader(http.StatusOK)
			w.Write(jsonConfig)
		})
	}
	failingHandler := func(t *testing.T, jsonConfig []byte) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusBadRequest)
			w.Write(jsonConfig)
		})
	}
	var testCases = []struct {
		name           string
		handlerWrapper func(t *testing.T, jsonConfig []byte) http.Handler
		expected       *api.ReleaseBuildConfiguration
		expectedError  bool
	}{
		{
			name:           "getting config works",
			handlerWrapper: correctHandler,
			expected:       parsedConfig,
			expectedError:  false,
		},
		{
			name:           "function errors on non OK status",
			handlerWrapper: failingHandler,
			expected:       nil,
			expectedError:  true,
		},
	}

	jsonConfig, err := json.Marshal(parsedConfig)
	if err != nil {
		t.Fatalf("%s: Failed to marshal parsedConfig to JSON: %v", t.Name(), err)
	}
	info := ResolverInfo{Org: "openshift", Repo: "hyperkube", Branch: "master"}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			server := httptest.NewServer(testCase.handlerWrapper(t, jsonConfig))
			info.Address = server.URL
			config, err := configFromResolver(&info)
			if err == nil && testCase.expectedError {
				t.Errorf("%s: expected an error, but got none", testCase.name)
			}
			if err != nil && !testCase.expectedError {
				t.Errorf("%s: expected no error, but got one: %v", testCase.name, err)
			}
			if !reflect.DeepEqual(config, testCase.expected) {
				t.Errorf("%s: didn't get correct config: %v", testCase.name, diff.ObjectReflectDiff(config, testCase.expected))
			}
			server.Close()
		})
	}
}

func TestRegistry(t *testing.T) {
	var (
		expectedReferences = map[string]api.LiteralTestStep{
			"ipi-deprovision-deprovision": {
				As:       "ipi-deprovision-deprovision",
				From:     "installer",
				Commands: "openshift-cluster destroy\n",
				Resources: api.ResourceRequirements{
					Requests: api.ResourceList{"cpu": "1000m", "memory": "2Gi"},
				},
			},
			"ipi-deprovision-must-gather": {
				As:       "ipi-deprovision-must-gather",
				From:     "installer",
				Commands: "gather\n",
				Resources: api.ResourceRequirements{
					Requests: api.ResourceList{"cpu": "1000m", "memory": "2Gi"},
				},
			},
			"ipi-install-install": {
				As:       "ipi-install-install",
				From:     "installer",
				Commands: "openshift-cluster install\n",
				Resources: api.ResourceRequirements{
					Requests: api.ResourceList{"cpu": "1000m", "memory": "2Gi"},
				},
			},
			"ipi-install-rbac": {
				As:       "ipi-install-rbac",
				From:     "installer",
				Commands: "setup-rbac\n",
				Resources: api.ResourceRequirements{
					Requests: api.ResourceList{"cpu": "1000m", "memory": "2Gi"},
				},
			},
		}

		deprovisionRef       = `ipi-deprovision-deprovision`
		deprovisionGatherRef = `ipi-deprovision-must-gather`
		installRef           = `ipi-install-install`
		installRBACRef       = `ipi-install-rbac`

		expectedChains = map[string][]api.TestStep{
			"ipi-install": {
				{
					Reference: &installRBACRef,
				}, {
					Reference: &installRef,
				},
			},
			"ipi-deprovision": {
				{
					Reference: &deprovisionGatherRef,
				}, {
					Reference: &deprovisionRef,
				},
			},
		}

		installChain     = `ipi-install`
		deprovisionChain = `ipi-deprovision`

		expectedWorkflows = map[string]api.MultiStageTestConfiguration{
			"ipi": {
				Pre: []api.TestStep{{
					Chain: &installChain,
				}},
				Post: []api.TestStep{{
					Chain: &deprovisionChain,
				}},
			},
		}

		testCases = []struct {
			name          string
			registryDir   string
			flatRegistry  bool
			references    map[string]api.LiteralTestStep
			chains        map[string][]api.TestStep
			workflows     map[string]api.MultiStageTestConfiguration
			expectedError bool
		}{{
			name:          "Read registry",
			registryDir:   "../../test/multistage-registry/registry",
			flatRegistry:  false,
			references:    expectedReferences,
			chains:        expectedChains,
			workflows:     expectedWorkflows,
			expectedError: false,
		}, {
			name:         "Read configmap style registry",
			registryDir:  "../../test/multistage-registry/configmap",
			flatRegistry: true,
			references: map[string]api.LiteralTestStep{
				installRef: expectedReferences[installRef],
			},
			chains:        map[string][]api.TestStep{},
			workflows:     map[string]api.MultiStageTestConfiguration{},
			expectedError: false,
		}}
	)

	for _, testCase := range testCases {
		references, chains, workflows, err := Registry(testCase.registryDir, testCase.flatRegistry)
		if err == nil && testCase.expectedError == true {
			t.Errorf("%s: got no error when error was expected", testCase.name)
		}
		if err != nil && testCase.expectedError == false {
			t.Errorf("%s: got error when error wasn't expected: %v", testCase.name, err)
		}
		if !reflect.DeepEqual(references, testCase.references) {
			t.Errorf("%s: output references different from expected: %s", testCase.name, diff.ObjectReflectDiff(references, testCase.references))
		}
		if !reflect.DeepEqual(chains, testCase.chains) {
			t.Errorf("%s: output chains different from expected: %s", testCase.name, diff.ObjectReflectDiff(chains, testCase.chains))
		}
		if !reflect.DeepEqual(workflows, testCase.workflows) {
			t.Errorf("%s: output workflows different from expected: %s", testCase.name, diff.ObjectReflectDiff(workflows, testCase.workflows))
		}
	}
}
