import http.client
import json
import unittest
from unittest.mock import Mock, patch
from matcher.telemetry import lag_samples
from matcher.kafka import KafkaRunner, observe
from matcher.health import Health
from matcher.worker import Delivery, Record, Processor
from matcher.contract import canonical
from matcher.facade import FacadeError
from helpers import event
from test_worker import FacadeDouble, PublisherDouble


class ProcessingTelemetryTests(unittest.TestCase):
    def test_committed_lag_ignores_other_partitions_and_unknown_is_not_zero(self):
        stats = {'topics': {'events': {'partitions': {
            '0': {'committed_offset': 8, 'ls_offset': 12, 'hi_offset': 20, 'consumer_lag': 4},
            '1': {'committed_offset': -1001, 'ls_offset': 12, 'consumer_lag': -1},
            '2': {'committed_offset': 5, 'ls_offset': 6, 'consumer_lag': 1},
        }}}}
        result = lag_samples(stats, {('events', 0), ('events', 1)})
        self.assertEqual(len(result), 2)
        self.assertEqual(result[0]['consumer_lag'], 4)
        self.assertEqual(result[0]['end_offset'], 12)
        self.assertIsNone(result[1]['consumer_lag'])
        self.assertFalse(result[1]['sample_available'])

    def test_statistics_sampling_is_throttled_and_errors_are_observable(self):
        observer = Mock()
        with patch('matcher.kafka.Consumer'):
            runner = KafkaRunner({}, topic='events', group_id='analytics', offset_reset='earliest',
                                 max_poll_ms=300000, session_timeout_ms=6000, workers=1,
                                 processor=Mock(), observer=observer, health=Health())
        try:
            runner.owned = {('events', 0)}
            raw = json.dumps({'topics': {'events': {'partitions': {'0': {
                'committed_offset': 1, 'ls_offset': 5, 'consumer_lag': 4}}}}})
            with patch('matcher.kafka.time.monotonic', return_value=1000):
                runner._stats(raw); runner._stats(raw)
            lag_calls = [c for c in observer.call_args_list if c.args[0] == 'consumer_lag']
            self.assertEqual(len(lag_calls), 1)
            self.assertEqual(lag_calls[0].kwargs['consumer_group'], 'analytics')
            runner._stats('not-json')
            self.assertEqual(observer.call_args.kwargs['code'], 'LAG_SAMPLE_FAILED')
        finally:
            runner.executor.shutdown()

    def test_observations_add_version_and_command_without_sensitive_fields(self):
        source = event()
        d = Delivery(Record('events', 0, 5, b'key', b'private-payload'), '2026-01-01', event=source)
        with self.assertLogs('matcher', level='INFO') as captured:
            observe('delivery_paused', d, code='FACADE_UNAVAILABLE', password='secret', original_event=source)
        value = json.loads(captured.records[0].message)
        self.assertEqual(value['telemetry_version'], 1)
        self.assertEqual(value['command_id'], source['command_id'])
        self.assertEqual(value['outcome'], 'RETRYING')
        self.assertNotIn('password', value); self.assertNotIn('original_event', value)
        self.assertNotIn('private-payload', captured.output[0])

    def test_facade_failure_and_retry_emit_latency_without_changing_recovery(self):
        facade, recorded = FacadeDouble(), []
        processor = Processor(facade, PublisherDouble(), max_bytes=100000, retry_base=1,
                              retry_max=4, max_refreshes=3,
                              observe=lambda *a, **kw: recorded.append((a, kw)))
        source = event()
        d = Delivery(Record('events', 0, 5, source['batch_id'].encode(), canonical(source)), '2026-01-01')
        processor.step(d)
        with patch.object(facade, 'prepare', side_effect=FacadeError()):
            self.assertGreater(processor.step(d), 0)
        self.assertEqual(recorded[-1][0][0], 'processing_step')
        self.assertEqual(recorded[-1][1]['outcome'], 'RETRYING')
        self.assertGreaterEqual(recorded[-1][1]['duration_ms'], 0)
        for _ in range(10):
            if d.stage == 'DONE': break
            processor.step(d)
        self.assertEqual(d.disposition, 'COMMITTED')
        self.assertTrue(any(a[0] == 'delivery_paused' for a, _ in recorded))

    def test_readiness_probe_logs_failure_then_success(self):
        observer = Mock()
        health = Health(observer=observer)
        server = health.serve(0)
        connection = http.client.HTTPConnection('127.0.0.1', server.server_port, timeout=2)
        try:
            with patch.object(health, 'state', side_effect=[False, True]):
                for expected in (503, 200):
                    connection.request('GET', '/readyz'); response=connection.getresponse()
                    self.assertEqual(response.status, expected); response.read()
            self.assertEqual([c.kwargs['ready'] for c in observer.call_args_list], [False, True])
        finally:
            connection.close(); server.shutdown(); server.server_close()
