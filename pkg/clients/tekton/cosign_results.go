package tekton

import (
	"context"
	"time"

	"k8s.io/apimachinery/pkg/util/wait"

	g "github.com/onsi/ginkgo/v2"
	"github.com/redhat-appstudio/e2e-tests/pkg/utils/tekton"
)

// AwaitAttestationAndSignature awaits attestation and signature.
func (t *TektonController) AwaitAttestationAndSignature(image string, timeout time.Duration) error {
	return wait.PollUntilContextTimeout(context.Background(), time.Second, timeout, true, func(ctx context.Context) (done bool, err error) {
		if _, err := tekton.FindCosignResultsForImage(image); err != nil {
			g.GinkgoWriter.Printf("failed to get cosign result for image %s: %+v\n", image, err)
			return false, nil
		}

		return true, nil
	})
}
