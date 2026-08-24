We should follow how the talos terraform provider is producing kubeconfigs (ephemeral)

- https://github.com/siderolabs/terraform-provider-talos/blob/main/pkg/talos/rfc6979.go
- https://github.com/siderolabs/terraform-provider-talos/blob/main/pkg/talos/talos_cluster_kubeconfig_ephemeral_resource.go

Kubeconfig should be an ephemeral output of the cluster resource.

---

I'll investigate how this repository uses RFC 6979 and how it maintains deterministic signatures for ephemeral resources.Now let me get more details on how the deterministic reader is created:## Summary

This Terraform provider uses RFC 6979 to achieve **deterministic, reproducible kubeconfigs** for ephemeral resources—the same inputs always produce identical output across multiple runs, with no randomness involved.

### How RFC 6979 is Used

1. **Custom ECDSA Signer**: The `deterministicECDSASigner` wrapper implements Go's `crypto.Signer` interface to use RFC 6979 deterministic nonce generation instead of random nonces. This is necessary because **Go 1.26+ ignores custom `io.Reader` parameters in `crypto/ecdsa` and always uses system randomness**.

2. **Deterministic Signatures**: The `rfc6979Sign()` function implements RFC 6979 Section 3.2, which derives the ECDSA signing nonce `k` deterministically from:
   - The private key `d`
   - The message hash
   - HMAC-SHA256 (via HMAC-DRBG)
   
   This ensures the same inputs always produce the same `(r, s)` signature values.

### Keeping Values Consistent Across Runs

The provider uses a **two-tier deterministic approach** for ephemeral kubeconfig resources:

1. **HKDF-based Seed Generation** (RFC 5869):
   - Creates a deterministic byte stream from:
     - **Secret**: K8s CA private key (stable in `machine_secrets`)
     - **Info**: Cluster name, endpoint, and validity period (notBefore/notAfter)
   
   ```go
   info := fmt.Sprintf("talos-kubeconfig:v1:%s:%s:%d:%d", clusterName, endpoint, notBefore.Unix(), notAfter.Unix())
   deterministicReader := hkdf.New(sha256.New, bundle.Certs.K8s.Key, []byte("talos-kubeconfig-v1"), []byte(info))
   ```

2. **Deterministic Derivation from HKDF Output**:
   - **Admin key**: 32 bytes from HKDF → parsed directly into P-256 ECDSA key (no randomness)
   - **Serial number**: 16 bytes from HKDF → deterministic certificate serial
   - **Certificate signature**: Signed with the CA key using `deterministicECDSASigner` (RFC 6979)

**Result**: Same cluster name + endpoint + time window always produces identical certificates and kubeconfigs, enabling Terraform's resource idempotency for ephemeral resources.