package image_test

import (
	"archive/tar"
	"context"
	"fmt"
	"io"
	"io/ioutil"
	"math/rand"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	dockertypes "github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	dockerClient "github.com/docker/docker/client"
	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/sclevine/spec"
	"github.com/sclevine/spec/report"

	"github.com/buildpack/lifecycle/image"
	h "github.com/buildpack/lifecycle/testhelpers"
)

var localTestRegistry *h.DockerRegistry

func TestLocal(t *testing.T) {
	rand.Seed(time.Now().UTC().UnixNano())

	localTestRegistry = h.NewDockerRegistry()
	localTestRegistry.Start(t)
	defer localTestRegistry.Stop(t)

	spec.Run(t, "local", testLocal, spec.Parallel(), spec.Report(report.Terminal{}))
}

func testLocal(t *testing.T, when spec.G, it spec.S) {
	var factory image.Factory
	var repoName string
	var dockerCli *dockerClient.Client

	it.Before(func() {
		var err error
		dockerCli = h.DockerCli(t)
		h.AssertNil(t, err)
		factory = image.Factory{
			Docker:   dockerCli,
			Out:      ioutil.Discard,
			Keychain: authn.DefaultKeychain,
		}
		repoName = "pack-image-test-" + h.RandString(10)
	})

	when("#NewLocal", func() {
		when("image is available locally", func() {
			it.After(func() {
				h.AssertNil(t, h.DockerRmi(dockerCli, repoName))
			})

			it("returns the local image", func() {
				repoName = "test/repo"
				labels := make(map[string]string)
				labels["some.label"] = "some.value"

				h.CreateImageOnLocal(t, dockerCli, repoName, fmt.Sprintf(`
							FROM scratch
							LABEL repo_name_for_randomisation=%s
							ENV MY_VAR=my_val
							`, repoName), labels)

				localImage, e := factory.NewLocal(repoName)
				h.AssertNil(t, e)

				labelValue, err := localImage.Label("some.label")
				h.AssertNil(t, err)
				h.AssertEq(t, labelValue, "some.value")
			})
		})

		when("image is not available locally", func() {
			it("returns an error", func() {
				repoName = "localhost:" + localTestRegistry.Port + "/pack-image-test-" + h.RandString(10)

				localImage, e := factory.NewLocal(repoName)
				h.AssertNil(t, e)

				_, err := localImage.Label("remote.label")
				h.AssertError(t, err, "does not exist")
			})
		})
	})

	when("#NewEmptyLocal", func() {
		it("returns a scratch image", func() {
			img := factory.NewEmptyLocal(repoName)

			t.Log("check that the empty image is useable image")
			h.AssertNil(t, img.SetLabel("some-key", "some-val"))
			_, err := img.Save()
			h.AssertNil(t, err)
		})
	})

	when("#label", func() {
		when("image exists", func() {
			it.Before(func() {
				h.CreateImageOnLocal(t, dockerCli, repoName, fmt.Sprintf(`
					FROM scratch
					LABEL repo_name_for_randomisation=%s
					LABEL mykey=myvalue other=data
				`, repoName), nil)
			})

			it.After(func() {
				h.AssertNil(t, h.DockerRmi(dockerCli, repoName))
			})

			it("returns the label value", func() {
				img, err := factory.NewLocal(repoName)
				h.AssertNil(t, err)

				label, err := img.Label("mykey")
				h.AssertNil(t, err)
				h.AssertEq(t, label, "myvalue")
			})

			it("returns an empty string for a missing label", func() {
				img, err := factory.NewLocal(repoName)
				h.AssertNil(t, err)

				label, err := img.Label("missing-label")
				h.AssertNil(t, err)
				h.AssertEq(t, label, "")
			})
		})

		when("image NOT exists", func() {
			it("returns an error", func() {
				img, err := factory.NewLocal(repoName)
				h.AssertNil(t, err)

				_, err = img.Label("mykey")
				h.AssertError(t, err, fmt.Sprintf("failed to get label, image '%s' does not exist", repoName))
			})
		})
	})

	when("#Env", func() {
		when("image exists", func() {
			it.Before(func() {
				h.CreateImageOnLocal(t, dockerCli, repoName, fmt.Sprintf(`
					FROM scratch
					LABEL repo_name_for_randomisation=%s
					ENV MY_VAR=my_val
				`, repoName), nil)
			})

			it.After(func() {
				h.AssertNil(t, h.DockerRmi(dockerCli, repoName))
			})

			it("returns the label value", func() {
				img, err := factory.NewLocal(repoName)
				h.AssertNil(t, err)

				val, err := img.Env("MY_VAR")
				h.AssertNil(t, err)
				h.AssertEq(t, val, "my_val")
			})

			it("returns an empty string for a missing label", func() {
				img, err := factory.NewLocal(repoName)
				h.AssertNil(t, err)

				val, err := img.Env("MISSING_VAR")
				h.AssertNil(t, err)
				h.AssertEq(t, val, "")
			})
		})

		when("image NOT exists", func() {
			it("returns an error", func() {
				img, err := factory.NewLocal(repoName)
				h.AssertNil(t, err)

				_, err = img.Env("MISSING_VAR")
				h.AssertError(t, err, fmt.Sprintf("failed to get env var, image '%s' does not exist", repoName))
			})
		})
	})

	when("#Name", func() {
		it("always returns the original name", func() {
			img, err := factory.NewLocal(repoName)
			h.AssertNil(t, err)

			h.AssertEq(t, img.Name(), repoName)
		})
	})

	when("#CreatedAt", func() {
		const reference = "busybox@sha256:2a03a6059f21e150ae84b0973863609494aad70f0a80eaeb64bddd8d92465812"
		it.Before(func() {
			// The SHA of a particular iteration of busybox:1.29
			err := h.PullImage(dockerCli, reference)
			h.AssertNil(t, err)
		})

		it("returns the containers created at time", func() {
			img, err := factory.NewLocal(reference)
			h.AssertNil(t, err)

			expectedTime := time.Date(2018, 10, 2, 17, 19, 34, 239926273, time.UTC)

			createdTime, err := img.CreatedAt()

			h.AssertNil(t, err)
			h.AssertEq(t, createdTime, expectedTime)
		})
	})

	when("#Digest", func() {
		const reference = "busybox@sha256:2a03a6059f21e150ae84b0973863609494aad70f0a80eaeb64bddd8d92465812"
		when("image exists and has a digest", func() {
			var expectedDigest string
			it.Before(func() {
				// The SHA of a particular iteration of busybox:1.29
				err := h.PullImage(dockerCli, reference)
				h.AssertNil(t, err)
			})

			it("returns the image digest", func() {
				expectedDigest = "sha256:2a03a6059f21e150ae84b0973863609494aad70f0a80eaeb64bddd8d92465812"
				img, err := factory.NewLocal(reference)
				h.AssertNil(t, err)
				digest, err := img.Digest()
				h.AssertNil(t, err)
				h.AssertEq(t, digest, expectedDigest)
			})
		})

		when("image exists but has no digest", func() {
			it.Before(func() {
				h.CreateImageOnLocal(t, dockerCli, repoName, fmt.Sprintf(`
					FROM scratch
					LABEL repo_name_for_randomisation=%s
					LABEL key=val
				`, repoName), nil)
			})

			it.After(func() {
				h.AssertNil(t, h.DockerRmi(dockerCli, repoName))
			})

			it("returns an empty string", func() {
				img, err := factory.NewLocal(repoName)
				h.AssertNil(t, err)
				digest, err := img.Digest()
				h.AssertNil(t, err)
				h.AssertEq(t, digest, "")
			})
		})
	})

	when("#SetLabel", func() {
		when("image exists", func() {
			var (
				img    image.Image
				origID string
			)
			it.Before(func() {
				var err error
				h.CreateImageOnLocal(t, dockerCli, repoName, fmt.Sprintf(`
					FROM scratch
					LABEL repo_name_for_randomisation=%s
					LABEL some-key=some-value
				`, repoName), nil)
				img, err = factory.NewLocal(repoName)
				h.AssertNil(t, err)
				origID = h.ImageID(t, repoName)
			})

			it.After(func() {
				h.AssertNil(t, h.DockerRmi(dockerCli, repoName, origID))
			})

			it("sets label and saves label to docker daemon", func() {
				h.AssertNil(t, img.SetLabel("somekey", "new-val"))
				t.Log("set label")
				label, err := img.Label("somekey")
				h.AssertNil(t, err)
				h.AssertEq(t, label, "new-val")
				t.Log("save label")
				_, err = img.Save()
				h.AssertNil(t, err)

				inspect, _, err := dockerCli.ImageInspectWithRaw(context.TODO(), repoName)
				h.AssertNil(t, err)
				label = inspect.Config.Labels["somekey"]
				h.AssertEq(t, strings.TrimSpace(label), "new-val")
			})
		})
	})

	when("#SetEnv", func() {
		var (
			img    image.Image
			origID string
		)
		it.Before(func() {
			var err error
			h.CreateImageOnLocal(t, dockerCli, repoName, fmt.Sprintf(`
					FROM scratch
					LABEL repo_name_for_randomisation=%s
					LABEL some-key=some-value
				`, repoName), nil)
			img, err = factory.NewLocal(repoName)
			h.AssertNil(t, err)
			origID = h.ImageID(t, repoName)
		})

		it.After(func() {
			h.AssertNil(t, h.DockerRmi(dockerCli, repoName, origID))
		})

		it("sets the environment", func() {
			err := img.SetEnv("ENV_KEY", "ENV_VAL")
			h.AssertNil(t, err)

			_, err = img.Save()
			h.AssertNil(t, err)

			inspect, _, err := dockerCli.ImageInspectWithRaw(context.TODO(), repoName)
			h.AssertNil(t, err)

			h.AssertContains(t, inspect.Config.Env, "ENV_KEY=ENV_VAL")
		})
	})

	when("#SetEntrypoint", func() {
		var (
			img    image.Image
			origID string
		)
		it.Before(func() {
			var err error
			h.CreateImageOnLocal(t, dockerCli, repoName, fmt.Sprintf(`
					FROM scratch
					LABEL repo_name_for_randomisation=%s
				`, repoName), nil)
			img, err = factory.NewLocal(repoName)
			h.AssertNil(t, err)
			origID = h.ImageID(t, repoName)
		})

		it.After(func() {
			h.AssertNil(t, h.DockerRmi(dockerCli, repoName, origID))
		})

		it("sets the entrypoint", func() {
			err := img.SetEntrypoint("some", "entrypoint")
			h.AssertNil(t, err)

			_, err = img.Save()
			h.AssertNil(t, err)

			inspect, _, err := dockerCli.ImageInspectWithRaw(context.TODO(), repoName)
			h.AssertNil(t, err)

			h.AssertEq(t, []string(inspect.Config.Entrypoint), []string{"some", "entrypoint"})
		})
	})

	when("#SetCmd", func() {
		var (
			img    image.Image
			origID string
		)

		it.Before(func() {
			var err error
			h.CreateImageOnLocal(t, dockerCli, repoName, fmt.Sprintf(`
					FROM scratch
					LABEL repo_name_for_randomisation=%s
				`, repoName), nil)
			img, err = factory.NewLocal(repoName)
			h.AssertNil(t, err)
			origID = h.ImageID(t, repoName)
		})

		it.After(func() {
			h.AssertNil(t, h.DockerRmi(dockerCli, repoName, origID))
		})

		it("sets the cmd", func() {
			err := img.SetCmd("some", "cmd")
			h.AssertNil(t, err)

			_, err = img.Save()
			h.AssertNil(t, err)

			inspect, _, err := dockerCli.ImageInspectWithRaw(context.TODO(), repoName)
			h.AssertNil(t, err)

			h.AssertEq(t, []string(inspect.Config.Cmd), []string{"some", "cmd"})
		})
	})

	when("#Rebase", func() {
		when("image exists", func() {
			var oldBase, oldTopLayer, newBase, origID string
			var origNumLayers int
			it.Before(func() {
				var wg sync.WaitGroup
				wg.Add(1)
				go func() {
					defer wg.Done()
					newBase = "pack-newbase-test-" + h.RandString(10)
					h.CreateImageOnLocal(t, dockerCli, newBase, fmt.Sprintf(`
						FROM busybox
						LABEL repo_name_for_randomisation=%s
						RUN echo new-base > base.txt
						RUN echo text-new-base > otherfile.txt
					`, newBase), nil)
				}()

				oldBase = "pack-oldbase-test-" + h.RandString(10)
				h.CreateImageOnLocal(t, dockerCli, oldBase, fmt.Sprintf(`
					FROM busybox
					LABEL repo_name_for_randomisation=%s
					RUN echo old-base > base.txt
					RUN echo text-old-base > otherfile.txt
				`, oldBase), nil)
				inspect, _, err := dockerCli.ImageInspectWithRaw(context.TODO(), oldBase)
				h.AssertNil(t, err)
				oldTopLayer = inspect.RootFS.Layers[len(inspect.RootFS.Layers)-1]

				h.CreateImageOnLocal(t, dockerCli, repoName, fmt.Sprintf(`
					FROM %s
					LABEL repo_name_for_randomisation=%s
					RUN echo text-from-image > myimage.txt
					RUN echo text-from-image > myimage2.txt
				`, oldBase, repoName), nil)
				inspect, _, err = dockerCli.ImageInspectWithRaw(context.TODO(), repoName)
				h.AssertNil(t, err)
				origNumLayers = len(inspect.RootFS.Layers)
				origID = inspect.ID

				wg.Wait()
			})

			it.After(func() {
				h.AssertNil(t, h.DockerRmi(dockerCli, repoName, oldBase, newBase, origID))
			})

			it("switches the base", func() {
				// Before
				txt, err := h.CopySingleFileFromImage(dockerCli, repoName, "base.txt")
				h.AssertNil(t, err)
				h.AssertEq(t, txt, "old-base\n")

				// Run rebase
				img, err := factory.NewLocal(repoName)
				h.AssertNil(t, err)
				newBaseImg, err := factory.NewLocal(newBase)
				h.AssertNil(t, err)
				err = img.Rebase(oldTopLayer, newBaseImg)
				h.AssertNil(t, err)
				_, err = img.Save()
				h.AssertNil(t, err)

				// After
				expected := map[string]string{
					"base.txt":      "new-base\n",
					"otherfile.txt": "text-new-base\n",
					"myimage.txt":   "text-from-image\n",
					"myimage2.txt":  "text-from-image\n",
				}
				ctr, err := dockerCli.ContainerCreate(context.Background(), &container.Config{Image: repoName}, &container.HostConfig{}, nil, "")
				defer dockerCli.ContainerRemove(context.Background(), ctr.ID, dockertypes.ContainerRemoveOptions{})
				for filename, expectedText := range expected {
					actualText, err := h.CopySingleFileFromContainer(dockerCli, ctr.ID, filename)
					h.AssertNil(t, err)
					h.AssertEq(t, actualText, expectedText)
				}

				// Final Image should have same number of layers as initial image
				inspect, _, err := dockerCli.ImageInspectWithRaw(context.TODO(), repoName)
				h.AssertNil(t, err)
				numLayers := len(inspect.RootFS.Layers)
				h.AssertEq(t, numLayers, origNumLayers)
			})
		})
	})

	when("#TopLayer", func() {
		when("image exists", func() {
			var expectedTopLayer string
			it.Before(func() {
				h.CreateImageOnLocal(t, dockerCli, repoName, fmt.Sprintf(`
				FROM busybox
				LABEL repo_name_for_randomisation=%s
				RUN echo old-base > base.txt
				RUN echo text-old-base > otherfile.txt
				`, repoName), nil)

				inspect, _, err := dockerCli.ImageInspectWithRaw(context.TODO(), repoName)
				h.AssertNil(t, err)
				expectedTopLayer = inspect.RootFS.Layers[len(inspect.RootFS.Layers)-1]
			})

			it.After(func() {
				h.AssertNil(t, h.DockerRmi(dockerCli, repoName))
			})

			it("returns the digest for the top layer (useful for rebasing)", func() {
				img, err := factory.NewLocal(repoName)
				h.AssertNil(t, err)

				actualTopLayer, err := img.TopLayer()
				h.AssertNil(t, err)

				h.AssertEq(t, actualTopLayer, expectedTopLayer)
			})
		})
	})

	when("#AddLayer", func() {
		var (
			tarPath string
			img     image.Image
			origID  string
		)
		it.Before(func() {
			h.CreateImageOnLocal(t, dockerCli, repoName, fmt.Sprintf(`
					FROM busybox
					LABEL repo_name_for_randomisation=%s
					RUN echo -n old-layer > old-layer.txt
				`, repoName), nil)
			tr, err := h.CreateSingleFileTar("/new-layer.txt", "new-layer")
			h.AssertNil(t, err)
			tarFile, err := ioutil.TempFile("", "add-layer-test")
			h.AssertNil(t, err)
			defer tarFile.Close()
			_, err = io.Copy(tarFile, tr)
			h.AssertNil(t, err)
			tarPath = tarFile.Name()

			img, err = factory.NewLocal(repoName)
			h.AssertNil(t, err)
			origID = h.ImageID(t, repoName)
		})

		it.After(func() {
			err := os.Remove(tarPath)
			h.AssertNil(t, err)
			h.AssertNil(t, h.DockerRmi(dockerCli, repoName, origID))
		})

		it("appends a layer", func() {
			err := img.AddLayer(tarPath)
			h.AssertNil(t, err)

			_, err = img.Save()
			h.AssertNil(t, err)

			output, err := h.CopySingleFileFromImage(dockerCli, repoName, "old-layer.txt")
			h.AssertNil(t, err)
			h.AssertEq(t, output, "old-layer")

			output, err = h.CopySingleFileFromImage(dockerCli, repoName, "new-layer.txt")
			h.AssertNil(t, err)
			h.AssertEq(t, output, "new-layer")
		})
	})

	when("#GetLayer", func() {
		when("the layer exists", func() {
			it.Before(func() {
				h.CreateImageOnLocal(t, dockerCli, repoName, fmt.Sprintf(`
					FROM busybox
					LABEL repo_name_for_randomisation=%s
					RUN mkdir /dir && echo -n file-contents > /dir/file.txt
				`, repoName), nil)
			})

			it("returns a layer tar", func() {
				img, err := factory.NewLocal(repoName)
				h.AssertNil(t, err)
				topLayer, err := img.TopLayer()
				h.AssertNil(t, err)
				r, err := img.GetLayer(topLayer)
				h.AssertNil(t, err)
				tr := tar.NewReader(r)
				header, err := tr.Next()
				h.AssertNil(t, err)
				h.AssertEq(t, header.Name, "dir/")
				header, err = tr.Next()
				h.AssertNil(t, err)
				h.AssertEq(t, header.Name, "dir/file.txt")
				contents := make([]byte, len("file-contents"), len("file-contents"))
				_, err = tr.Read(contents)
				if err != io.EOF {
					t.Fatalf("expected end of file: %x", err)
				}
				h.AssertEq(t, string(contents), "file-contents")
			})
		})

		when("the layer doesn't exist", func() {
			it.Before(func() {
				h.CreateImageOnLocal(t, dockerCli, repoName, fmt.Sprintf(`
					FROM busybox
					LABEL repo_name_for_randomisation=%s
				`, repoName), nil)
			})

			it("returns an error", func() {
				img, err := factory.NewLocal(repoName)
				h.AssertNil(t, err)
				_, err = img.GetLayer("not-exist")
				h.AssertError(
					t,
					err,
					fmt.Sprintf("image '%s' does not contain layer with diff ID 'not-exist'", repoName),
				)
			})
		})
	})

	when("#ReuseLayer", func() {
		var (
			layer1SHA string
			layer2SHA string
			img       image.Image
			origID    string
		)
		it.Before(func() {
			var err error

			h.CreateImageOnLocal(t, dockerCli, repoName, fmt.Sprintf(`
					FROM busybox
					LABEL repo_name_for_randomisation=%s
					RUN echo -n old-layer-1 > layer-1.txt
					RUN echo -n old-layer-2 > layer-2.txt
				`, repoName), nil)

			inspect, _, err := dockerCli.ImageInspectWithRaw(context.TODO(), repoName)
			h.AssertNil(t, err)
			origID = inspect.ID

			layer1SHA = inspect.RootFS.Layers[1]
			layer2SHA = inspect.RootFS.Layers[2]

			img, err = factory.NewLocal("busybox")
			h.AssertNil(t, err)

			img.Rename(repoName)
			h.AssertNil(t, err)
		})

		it.After(func() {
			h.AssertNil(t, h.DockerRmi(dockerCli, repoName, origID))
		})

		it("reuses a layer", func() {
			err := img.ReuseLayer(layer2SHA)
			h.AssertNil(t, err)

			_, err = img.Save()
			h.AssertNil(t, err)

			output, err := h.CopySingleFileFromImage(dockerCli, repoName, "layer-2.txt")
			h.AssertNil(t, err)
			h.AssertEq(t, output, "old-layer-2")

			// Confirm layer-1.txt does not exist
			_, err = h.CopySingleFileFromImage(dockerCli, repoName, "layer-1.txt")
			h.AssertMatch(t, err.Error(), regexp.MustCompile(`Error: No such container:path: .*:layer-1.txt`))
		})

		it("does not download the old image if layers are directly above (performance)", func() {
			err := img.ReuseLayer(layer1SHA)
			h.AssertNil(t, err)

			_, err = img.Save()
			h.AssertNil(t, err)

			output, err := h.CopySingleFileFromImage(dockerCli, repoName, "layer-1.txt")
			h.AssertNil(t, err)
			h.AssertEq(t, output, "old-layer-1")

			// Confirm layer-2.txt does not exist
			_, err = h.CopySingleFileFromImage(dockerCli, repoName, "layer-2.txt")
			h.AssertMatch(t, err.Error(), regexp.MustCompile(`Error: No such container:path: .*:layer-2.txt`))
		})
	})

	when("#Save", func() {
		var (
			img     image.Image
			origID  string
			tarPath string
		)

		when("image exists", func() {
			it.Before(func() {
				var err error
				h.CreateImageOnLocal(t, dockerCli, repoName, fmt.Sprintf(`
					FROM busybox
					LABEL repo_name_for_randomisation=%s
					LABEL mykey=oldValue
				`, repoName), nil)
				img, err = factory.NewLocal(repoName)
				h.AssertNil(t, err)
				origID = h.ImageID(t, repoName)

				tr, err := h.CreateSingleFileTar("/new-layer.txt", "new-layer")
				h.AssertNil(t, err)

				tarFile, err := ioutil.TempFile("", "add-layer-test")
				h.AssertNil(t, err)
				defer tarFile.Close()

				_, err = io.Copy(tarFile, tr)
				h.AssertNil(t, err)

				tarPath = tarFile.Name()
			})

			it.After(func() {
				os.Remove(tarPath)
				h.AssertNil(t, h.DockerRmi(dockerCli, repoName, origID))
			})

			it("returns the image digest", func() {
				err := img.SetLabel("mykey", "newValue")
				h.AssertNil(t, err)

				err = img.AddLayer(tarPath)
				h.AssertNil(t, err)

				imgDigest, err := img.Save()
				h.AssertNil(t, err)

				inspect, _, err := dockerCli.ImageInspectWithRaw(context.TODO(), imgDigest)
				h.AssertNil(t, err)
				label := inspect.Config.Labels["mykey"]
				h.AssertEq(t, strings.TrimSpace(label), "newValue")

				t.Log("image has history")
				history, err := dockerCli.ImageHistory(context.TODO(), imgDigest)
				h.AssertNil(t, err)
				h.AssertEq(t, len(history), len(inspect.RootFS.Layers))
			})
		})
	})

	when("#Found", func() {
		when("it exists", func() {
			it.Before(func() {
				h.CreateImageOnLocal(t, dockerCli, repoName, fmt.Sprintf(`
					FROM scratch
					LABEL repo_name_for_randomisation=%s
				`, repoName), nil)
			})

			it.After(func() {
				h.DockerRmi(dockerCli, repoName)
			})

			it("returns true, nil", func() {
				image, err := factory.NewLocal(repoName)
				exists, err := image.Found()

				h.AssertNil(t, err)
				h.AssertEq(t, exists, true)
			})
		})

		when("it does not exist", func() {
			it("returns false, nil", func() {
				image, err := factory.NewLocal(repoName)
				exists, err := image.Found()

				h.AssertNil(t, err)
				h.AssertEq(t, exists, false)
			})
		})
	})

	when("#Delete", func() {
		when("the image does not exist", func() {
			it("should not error", func() {
				img, err := factory.NewLocal("image-does-not-exist")
				h.AssertNil(t, err)

				h.AssertNil(t, img.Delete())
			})
		})

		when("the image does exist", func() {
			var (
				origImg image.Image
				origID  string
			)

			it.Before(func() {
				var err error
				h.CreateImageOnLocal(t, dockerCli, repoName, fmt.Sprintf(`
					FROM busybox
					LABEL repo_name_for_randomisation=%s
					LABEL mykey=oldValue
				`, repoName), nil)
				origImg, err = factory.NewLocal(repoName)
				h.AssertNil(t, err)

				origID = h.ImageID(t, repoName)
			})

			it("should delete the image", func() {
				found, err := origImg.Found()
				h.AssertNil(t, err)
				h.AssertEq(t, found, true)

				h.AssertNil(t, origImg.Delete())

				img, err := factory.NewLocal(origID)
				h.AssertNil(t, err)

				found, err = img.Found()
				h.AssertNil(t, err)
				h.AssertEq(t, found, false)
			})

			when("the image has been re-tagged", func() {
				const newTag = "different-tag"

				it.Before(func() {
					h.AssertNil(t, dockerCli.ImageTag(context.TODO(), origImg.Name(), newTag))

					_, err := dockerCli.ImageRemove(context.TODO(), origImg.Name(), dockertypes.ImageRemoveOptions{})
					h.AssertNil(t, err)
				})

				it("should delete the image", func() {
					found, err := origImg.Found()
					h.AssertNil(t, err)
					h.AssertEq(t, found, true)

					h.AssertNil(t, origImg.Delete())

					origImg, err = factory.NewLocal(newTag)
					h.AssertNil(t, err)

					found, err = origImg.Found()
					h.AssertNil(t, err)
					h.AssertEq(t, found, false)
				})
			})
		})
	})
}
