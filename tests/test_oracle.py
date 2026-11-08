"""Exercise the real random oracle CLI with synthetic requests."""
import contextlib
import base64
import io
import json
from datetime import datetime, timedelta, timezone
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import call, patch

import cv2
import numpy as np

import main
from booking_tools.batch import slider_distance
from booking_tools.oracle import generate_random_reply, generate_reference_reply, process_request, run_random, run_stdio


class RandomOracleTests(unittest.TestCase):
    def test_stdio_reference_reuses_process_without_reusing_answers(self):
        with tempfile.TemporaryDirectory() as directory:
            request = self.request(Path(directory) / 'request.json')
            expected = slider_distance(request['challenge']['bg'], request['challenge']['piece'], 300)
            worker = subprocess.Popen([sys.executable, str(Path(main.__file__).resolve()),
                                       'oracle-reference', '--stdio'], cwd=directory,
                                      stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                                      text=True, encoding='utf-8')
            try:
                self.assertEqual(json.loads(worker.stdout.readline()), {'ready': True})
                for account, token in (('one', 'token-one'), ('two', 'token-two'), ('one', 'new-token')):
                    request['account'] = account
                    request['challenge']['token'] = token
                    worker.stdin.write(json.dumps(request) + '\n')
                    worker.stdin.flush()
                    self.assertEqual(json.loads(worker.stdout.readline()), {'token': token, 'x': expected})
                    self.assertIsNone(worker.poll())
                worker.stdin.close()
                self.assertEqual(worker.wait(timeout=5), 0)
                self.assertEqual(worker.stdout.read(), '')
                self.assertEqual(worker.stderr.read(), '')
                self.assertFalse(Path(directory, 'request.json.reply').exists())
            finally:
                if worker.poll() is None:
                    worker.kill()
                worker.wait(timeout=5)
                for stream in (worker.stdin, worker.stdout, worker.stderr):
                    stream.close()

    def test_stdio_random_and_failure_stop(self):
        with tempfile.TemporaryDirectory() as directory:
            request = self.request(Path(directory) / 'request.json')
            reference = slider_distance(request['challenge']['bg'], request['challenge']['piece'], 300)
            sink = io.StringIO()
            source = io.StringIO(json.dumps(request) + '\n{}\n' + json.dumps(request) + '\n')
            self.assertEqual(run_stdio(source=source, sink=sink), 1)
            replies = [json.loads(line) for line in sink.getvalue().splitlines()]
            self.assertEqual(len(replies), 3)  # ready, answer, cancellation; no fourth reply.
            self.assertEqual(replies[0], {'ready': True})
            self.assertGreater(abs(replies[1]['x'] - reference), 10)
            self.assertEqual(replies[2], {'cancelled': True})

    def test_reference_reply_and_expiration_after_computation(self):
        background = np.zeros((150, 300, 3), dtype=np.uint8)
        piece = np.zeros((50, 50, 4), dtype=np.uint8)
        with patch('booking_tools.oracle.slider_distance', return_value=94), \
                patch('booking_tools.oracle.random.randrange') as choose:
            self.assertEqual(generate_reference_reply(background, piece, token='reference'),
                             {'token': 'reference', 'x': 94})
            choose.assert_not_called()
        with tempfile.TemporaryDirectory() as directory:
            request = self.request(Path(directory) / 'request.json')
            class Clock:
                fromisoformat = datetime.fromisoformat
                now = staticmethod(lambda tz: times.pop(0))
            times = [datetime.now(timezone.utc), datetime.now(timezone.utc) + timedelta(minutes=2)]
            with patch('booking_tools.oracle.datetime', Clock):
                with self.assertRaisesRegex(ValueError, 'expired deadline'):
                    process_request(request, mode='reference')

    def image_data(self, width, height, channels, extension):
        image = np.zeros((height, width, channels), dtype=np.uint8)
        success, encoded = cv2.imencode(extension, image)
        self.assertTrue(success)
        return base64.b64encode(encoded).decode('ascii')

    def request(self, path):
        background = np.full((150, 300, 3), 40, dtype=np.uint8)
        piece = np.zeros((50, 50, 4), dtype=np.uint8)
        cv2.rectangle(piece, (5, 5), (44, 44), (70, 100, 50, 255), -1)
        cv2.rectangle(background, (130, 55), (169, 94), (225, 225, 225), 1)
        value = {
            'account': 'synthetic',
            'deadline': (datetime.now(timezone.utc) + timedelta(minutes=1)).isoformat(),
            'challenge': {
                'token': 'synthetic-token',
                'bg': base64.b64encode(cv2.imencode('.jpg', background)[1]).decode('ascii'),
                'piece': base64.b64encode(cv2.imencode('.png', piece)[1]).decode('ascii'),
            },
        }
        path.write_text(json.dumps(value), encoding='utf-8')
        return value

    def test_cli_from_another_directory(self):
        with tempfile.TemporaryDirectory(prefix='oracle test ') as directory:
            path = Path(directory) / 'request.json'
            request = self.request(path)
            reference = slider_distance(request['challenge']['bg'], request['challenge']['piece'], 300)
            result = subprocess.run(
                [sys.executable, str(Path(main.__file__).resolve()), 'oracle-random',
                 '--request', str(path), '--exclusion-radius', '37'],
                cwd=directory, capture_output=True, timeout=10)
            self.assertEqual(result.returncode, 0)
            self.assertEqual(result.stdout, b'')
            self.assertEqual(result.stderr, b'')
            reply = json.loads(Path(str(path) + '.reply').read_text(encoding='utf-8'))
            self.assertEqual(reply['token'], 'synthetic-token')
            self.assertIs(type(reply['x']), int)
            self.assertGreaterEqual(reply['x'], 0)
            self.assertLessEqual(reply['x'], 250)
            self.assertGreater(abs(reply['x'] - reference), 37)

    def test_coordinate_bounds(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / 'request.json'
            self.request(path)
            for rank, coordinate in ((0, 0), (229, 250)):
                with patch('booking_tools.oracle.slider_distance', return_value=94), \
                        patch('booking_tools.oracle.random.randrange', return_value=rank) as choose:
                    self.assertEqual(run_random(path), 0)
                    choose.assert_called_once_with(230)
                reply = json.loads(Path(str(path) + '.reply').read_text(encoding='utf-8'))
                self.assertEqual(reply['x'], coordinate)

    def test_exclusion_clips_to_image_bounds_and_preserves_remaining_endpoints(self):
        # Clip either side, exercise a one-pixel remainder and a bound > 300.
        cases = ((300, 50, 94, 10), (300, 50, 0, 10), (300, 50, 250, 10),
                 (800, 50, 400, 10), (51, 50, 0, 0))
        for bg_width, piece_width, reference, radius in cases:
            background = np.zeros((150, bg_width), dtype=np.uint8)
            piece = np.zeros((50, piece_width, 4), dtype=np.uint8)
            allowed = [x for x in range(bg_width-piece_width+1) if abs(x-reference) > radius]
            for rank, coordinate in ((0, allowed[0]), (len(allowed)-1, allowed[-1])):
                with self.subTest(bg_width=bg_width, reference=reference, radius=radius, coordinate=coordinate):
                    with patch('booking_tools.oracle.slider_distance', return_value=reference), \
                            patch('booking_tools.oracle.random.randrange', return_value=rank) as choose:
                        reply = generate_random_reply(background, piece, token='synthetic-token', exclusion_radius=radius)
                    choose.assert_called_once_with(len(allowed))
                    self.assertEqual(reply, {'token': 'synthetic-token', 'x': coordinate})

    def test_every_allowed_coordinate_has_exactly_one_random_rank(self):
        background = np.zeros((150, 300, 3), dtype=np.uint8)
        piece = np.zeros((50, 50, 4), dtype=np.uint8)
        expected = set(range(251)) - set(range(84, 105))
        with patch('booking_tools.oracle.slider_distance', return_value=94), \
                patch('booking_tools.oracle.random.randrange', side_effect=range(len(expected))) as choose:
            outputs = [generate_random_reply(background, piece, token='synthetic-token')['x']
                       for _ in expected]
        self.assertEqual(set(outputs), expected)
        self.assertEqual(len(outputs), len(set(outputs)))
        self.assertEqual(choose.call_args_list, [call(len(expected))] * len(expected))

    def test_no_remaining_coordinate_produces_no_reply(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / 'request.json'
            self.request(path)
            with patch('booking_tools.oracle.random.randrange') as choose, contextlib.redirect_stdout(io.StringIO()):
                self.assertEqual(run_random(path, exclusion_radius=250), 1)
            choose.assert_not_called()
            self.assertFalse(Path(str(path) + '.reply').exists())
        equal_width = np.zeros((50, 50), dtype=np.uint8)
        with patch('booking_tools.oracle.slider_distance', return_value=0), \
                patch('booking_tools.oracle.random.randrange') as choose:
            with self.assertRaisesRegex(ValueError, 'no legal random coordinate'):
                generate_random_reply(equal_width, equal_width, token='synthetic', exclusion_radius=0)
            choose.assert_not_called()

    def test_solver_failure_does_not_fall_back_to_unrestricted_random(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / 'request.json'
            request = self.request(path)
            # Real solver rejects a featureless background; no mock of the algorithm.
            request['challenge']['bg'] = self.image_data(300, 150, 3, '.jpg')
            path.write_text(json.dumps(request), encoding='utf-8')
            with patch('booking_tools.oracle.random.randrange') as choose, contextlib.redirect_stdout(io.StringIO()):
                self.assertEqual(run_random(path), 1)
            choose.assert_not_called()
            self.assertFalse(Path(str(path) + '.reply').exists())

    def test_invalid_radius_or_solver_position_is_rejected(self):
        background = np.zeros((150, 300, 3), dtype=np.uint8)
        piece = np.zeros((50, 50, 4), dtype=np.uint8)
        for radius in (-1, 1.5, None, True):
            with patch('booking_tools.oracle.slider_distance') as solve, \
                    patch('booking_tools.oracle.random.randrange') as choose:
                with self.assertRaises(ValueError):
                    generate_random_reply(background, piece, token='synthetic', exclusion_radius=radius)
                solve.assert_not_called()
                choose.assert_not_called()
        for reference in (-1, 251, None, True):
            with patch('booking_tools.oracle.slider_distance', return_value=reference), \
                    patch('booking_tools.oracle.random.randrange') as choose:
                with self.assertRaises(ValueError):
                    generate_random_reply(background, piece, token='synthetic')
                choose.assert_not_called()

    def test_reply_rejects_invalid_images_or_token_before_random_choice(self):
        background = np.zeros((150, 300, 3), dtype=np.uint8)
        piece = np.zeros((50, 50, 4), dtype=np.uint8)
        invalid_images = (None, [], np.zeros((0, 10)), np.zeros((10, 0)), np.zeros(10), np.zeros((1, 1, 1, 1)))
        cases = [(bad, piece, 'token') for bad in invalid_images]
        cases += [(background, bad, 'token') for bad in invalid_images]
        cases += [(background, np.zeros((50, 301)), 'token'),
                  (background, np.zeros((151, 50)), 'token')]
        cases += [(background, piece, token) for token in ('', None, 42)]
        for bg, part, token in cases:
            with patch('booking_tools.oracle.random.randrange') as choose:
                with self.assertRaises(ValueError):
                    generate_random_reply(bg, part, token=token)
                choose.assert_not_called()

    def test_invalid_or_expired_request_produces_no_reply(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / 'request.json'
            value = self.request(path)
            cases = [
                {},
                dict(value, deadline=(datetime.now(timezone.utc) - timedelta(seconds=1)).isoformat()),
                dict(value, deadline='2099-01-01T12:00:00'),
                dict(value, challenge={'token': ''}),
            ]
            for request in cases:
                path.write_text(json.dumps(request), encoding='utf-8')
                with contextlib.redirect_stdout(io.StringIO()) as output:
                    self.assertEqual(run_random(path), 1)
                self.assertEqual(output.getvalue(), 'oracle_random_failed\n')
                self.assertFalse(Path(str(path) + '.reply').exists())

    def test_missing_or_invalid_images_produce_no_answer(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / 'request.json'
            invalid_values = [None, '', 12, [], 'invalid base64!',
                              base64.b64encode(b'not an image').decode('ascii'),
                              base64.b64encode(b'\x89PNG\r\n\x1a\n').decode('ascii')]
            for field in ('bg', 'piece'):
                for encoded in invalid_values:
                    with self.subTest(field=field, value=encoded):
                        request = self.request(path)
                        if encoded is None:
                            del request['challenge'][field]
                        else:
                            request['challenge'][field] = encoded
                        path.write_text(json.dumps(request), encoding='utf-8')
                        with patch('booking_tools.oracle.random.randrange') as choose, contextlib.redirect_stdout(io.StringIO()):
                            self.assertEqual(run_random(path), 1)
                        choose.assert_not_called()
                        self.assertFalse(Path(str(path) + '.reply').exists())

    def test_piece_must_fit_background(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / 'request.json'
            for width, height in ((301, 50), (50, 151)):
                with self.subTest(width=width, height=height):
                    request = self.request(path)
                    request['challenge']['piece'] = self.image_data(width, height, 4, '.png')
                    path.write_text(json.dumps(request), encoding='utf-8')
                    with contextlib.redirect_stdout(io.StringIO()):
                        self.assertEqual(run_random(path), 1)
                    self.assertFalse(Path(str(path) + '.reply').exists())


if __name__ == '__main__':
    unittest.main()
