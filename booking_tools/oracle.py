"""本机 oracle：同一处理函数支持单次文件调用及预热后的串行 stdio 调用。"""
import base64
import json
import random
import sys
from datetime import datetime, timezone
from pathlib import Path

import cv2
import numpy as np

from .session import write_json
from .batch import slider_distance


def decode_challenge_image(encoded, field):
    """将原始 Base64 图片解码为像素数组，拒绝空值、非法编码和损坏图片。"""
    if not isinstance(encoded, str) or not encoded:
        raise ValueError(f'{field} must contain base64 image data')
    image_bytes = base64.b64decode(encoded, validate=True)
    if not image_bytes:
        raise ValueError(f'{field} contains no image bytes')
    # 保留灰度、彩色或透明通道，避免在读取拼图时丢失其透明区域。
    image = cv2.imdecode(np.frombuffer(image_bytes, dtype=np.uint8), cv2.IMREAD_UNCHANGED)
    if image is None or image.size == 0:
        raise ValueError(f'{field} is not a decodable image')
    return image


def generate_reference_reply(background, piece, *, token):
    """返回既有算法的原图估计坐标；两张图片为已解码数组，不读写文件。

    x 是完整拼图左边缘（包括透明边缘）的原图横坐标。
    token 必须由当前请求传入；算法估计不等于服务器验证结果。
    """
    if not isinstance(token, str) or not token:
        raise ValueError('missing challenge token')
    for field, image in (('bg', background), ('piece', piece)):
        if not isinstance(image, np.ndarray) or image.ndim not in (2, 3) or image.size == 0:
            raise ValueError(f'{field} must be a nonempty decoded image')
    if piece.shape[0] > background.shape[0] or piece.shape[1] > background.shape[1]:
        raise ValueError('piece dimensions exceed background dimensions')

    # 既有算法接收Base64；无损PNG适配保留像素与透明通道，不改求解阈值。
    encoded_images = []
    for image in (background, piece):
        success, encoded = cv2.imencode('.png', image)
        if not success:
            raise ValueError('cannot encode decoded image for existing solver')
        encoded_images.append(base64.b64encode(encoded).decode('ascii'))
    reference_x = slider_distance(*encoded_images, rendered_width=background.shape[1])
    max_x = background.shape[1] - piece.shape[1]
    if type(reference_x) is not int or not 0 <= reference_x <= max_x:
        raise ValueError('solver position is outside image bounds')
    return {'token': token, 'x': reference_x}


def generate_random_reply(background, piece, *, token, exclusion_radius=10):
    """排除估计坐标附近闭区间，在剩余合法整数中均匀随机选取。

    默认半径10只是测试参数；求解失败或没有剩余位置时不生成答案。
    此入口保持随机语义，估计坐标入口为 generate_reference_reply。
    """
    if type(exclusion_radius) is not int or exclusion_radius < 0:
        raise ValueError('exclusion_radius must be a nonnegative integer')
    reference_x = generate_reference_reply(background, piece, token=token)['x']
    max_x = background.shape[1] - piece.shape[1]

    # 排除闭区间 [reference_x-radius, reference_x+radius]，并裁到合法范围。
    lower = max(0, reference_x - exclusion_radius)
    upper = min(max_x, reference_x + exclusion_radius)
    remaining = lower + max_x - upper
    if remaining == 0:
        raise ValueError('exclusion leaves no legal random coordinate')

    # 对剩余位置编号再映射回x，左右区间的每个整数概率相同，无需反复抽样。
    rank = random.randrange(remaining)
    x = rank if rank < lower else rank + (upper - lower + 1)
    return {'token': token, 'x': x}


def process_request(request, *, mode='random', exclusion_radius=10):
    """处理单个挑战；worker 不保存上一个账号、token 或答案。"""
    deadline = datetime.fromisoformat(request['deadline'])
    if deadline.utcoffset() is None or datetime.now(timezone.utc) >= deadline:
        raise ValueError('expired deadline')
    challenge = request['challenge']
    background = decode_challenge_image(challenge['bg'], 'bg')
    piece = decode_challenge_image(challenge['piece'], 'piece')
    if mode == 'reference':
        reply = generate_reference_reply(background, piece, token=challenge['token'])
    elif mode == 'random':
        reply = generate_random_reply(background, piece, token=challenge['token'],
                                      exclusion_radius=exclusion_radius)
    else:
        raise ValueError('unknown oracle mode')
    # 长耗时求解也必须服从本次请求的截止时间。
    if datetime.now(timezone.utc) >= deadline:
        raise ValueError('expired deadline')
    return reply


def run_random(request_path, *, exclusion_radius=10, mode='random'):
    """处理一次请求，成功写入答案返回0，输入无效或处理失败返回1。"""
    path = Path(request_path)
    try:
        request = json.loads(path.read_text(encoding='utf-8'))
        reply = process_request(request, mode=mode, exclusion_radius=exclusion_radius)
        # 原子替换确保调用方只能读取完整答案。
        write_json(Path(str(path) + '.reply'), reply)
    except (OSError, ValueError, KeyError, TypeError, cv2.error):
        # 只输出固定错误标识，异常内容可能包含挑战数据。
        print('oracle_random_failed')
        return 1
    return 0


def run_stdio(*, mode='random', exclusion_radius=10, source=None, sink=None):
    """依赖加载完才发 ready；一行请求对应一行答案，全程串行、无临时文件。

    EOF 结束本轮。失败发 cancelled 并退出，调用方停止流程而非重启重试。
    stdout 只用于协议，不输出图片、token 或异常文本到日志。
    """
    source = source if source is not None else sys.stdin
    sink = sink if sink is not None else sys.stdout
    def send(value):
        sink.write(json.dumps(value, ensure_ascii=True, separators=(',', ':')) + '\n')
        sink.flush()
    send({'ready': True})
    for line in source:
        try:
            reply = process_request(json.loads(line), mode=mode, exclusion_radius=exclusion_radius)
        except (OSError, ValueError, KeyError, TypeError, cv2.error):
            send({'cancelled': True})
            return 1
        send(reply)
    return 0
