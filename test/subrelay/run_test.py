import ssl
import unittest
from unittest.mock import Mock, patch

from run import failed_http_challenge, require_issuance_failure


class CertificateNegativeControl(unittest.TestCase):
    def control(self, probe, ready=None, evidence=True):
        require_issuance_failure(
            probe,
            listener_ready=ready or Mock(),
            challenge_failed=Mock(return_value=evidence),
        )

    def test_successful_trusted_handshake_always_fails_control(self):
        handshake = Mock(return_value=None)
        with self.assertRaisesRegex(AssertionError, "certificate issued"):
            self.control(handshake)
        handshake.assert_called_once_with()

    def test_tls_failure_with_ready_listener_and_failed_challenge_passes(self):
        with patch("run.time.monotonic", side_effect=[0, 0, 13]), patch("run.time.sleep"):
            self.control(Mock(side_effect=ssl.SSLError("certificate unavailable")))

    def test_dead_listener_cannot_be_mistaken_for_failed_validation(self):
        with self.assertRaisesRegex(AssertionError, "listener unavailable"):
            self.control(Mock(side_effect=ConnectionRefusedError("edge exited")))

    def test_tls_errors_without_failed_http_challenge_do_not_pass(self):
        with patch("run.time.monotonic", side_effect=[0, 0, 13]), patch("run.time.sleep"):
            with self.assertRaisesRegex(AssertionError, "no failed HTTP-01"):
                self.control(Mock(side_effect=ssl.SSLError("no certificate")), evidence=False)

    def test_listener_exit_after_handshake_failure_does_not_pass(self):
        with self.assertRaisesRegex(AssertionError, "edge exited"):
            self.control(Mock(side_effect=ssl.SSLError("failed")), ready=Mock(side_effect=[None, AssertionError("edge exited")]))

    def test_unexpected_probe_failure_is_not_a_successful_control(self):
        with self.assertRaisesRegex(AssertionError, "HTTP profile failed"):
            self.control(Mock(side_effect=AssertionError("HTTP profile failed")))

    def test_evidence_requires_http_attempt_followed_by_invalid_completion(self):
        invalid = "authz abc set INVALID by completed challenge xyz"
        http = "Attempting to validate w/ HTTP: http://sub.test:81/.well-known/acme-challenge/token"
        self.assertFalse(failed_http_challenge(invalid))
        self.assertFalse(failed_http_challenge(invalid + "\n" + http))
        self.assertTrue(failed_http_challenge(http + "\n" + invalid))
