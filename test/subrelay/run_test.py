import ssl
import unittest
from unittest.mock import Mock, patch

from run import require_issuance_failure


class CertificateNegativeControl(unittest.TestCase):
    def test_successful_tls_handshake_fails_control_without_any_http_check(self):
        handshake = Mock(return_value=None)
        with self.assertRaisesRegex(AssertionError, "certificate issued"):
            require_issuance_failure(handshake)
        handshake.assert_called_once_with()

    def test_only_tls_errors_count_as_failed_issuance(self):
        handshake = Mock(side_effect=ssl.SSLError("certificate unavailable"))
        with patch("run.time.monotonic", side_effect=[0, 0, 13]), patch("run.time.sleep"):
            require_issuance_failure(handshake)
        handshake.assert_called_once_with()

    def test_unexpected_probe_failure_is_not_a_successful_control(self):
        with self.assertRaisesRegex(AssertionError, "HTTP profile failed"):
            require_issuance_failure(Mock(side_effect=AssertionError("HTTP profile failed")))
