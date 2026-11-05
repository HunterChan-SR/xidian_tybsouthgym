import base64
import unittest
import cv2
import numpy as np
from booking_tools.batch import slider_distance

def encoded(image):
    return base64.b64encode(cv2.imencode('.png', image)[1]).decode()

class SliderTests(unittest.TestCase):
    def images(self):
        background = np.full((150, 400, 3), 40, dtype=np.uint8)
        piece = np.zeros((150, 70, 4), dtype=np.uint8)
        polygon = np.array([[2,25],[20,25],[20,10],[40,10],[40,25],[55,25],[55,65],[2,65]], np.int32)
        cv2.fillPoly(piece, [polygon], (70,100,50,255))
        cv2.polylines(background, [polygon + [170,0]], True, (225,225,225), 1)
        return background, piece

    def test_scaled_outline(self):
        background, piece = self.images()
        distance = slider_distance(encoded(background), encoded(piece), 280)
        self.assertLessEqual(abs(distance - 119), 2)

    def test_no_unique_match(self):
        background, piece = self.images()
        for fill in (0,255):
            background[:] = fill
            with self.assertRaises(ValueError):
                slider_distance(encoded(background), encoded(piece), 280)

if __name__ == '__main__':
    unittest.main()
