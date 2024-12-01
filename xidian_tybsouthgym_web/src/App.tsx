import  {useRef, useState } from 'react';
import { Form, InputNumber, Select, Button, message, Input, Switch } from 'antd';
import TextArea from 'antd/es/input/TextArea';
import 'react-h5-audio-player/lib/styles.css';
const { Option } = Select;
const apiurl = "/api"

function App() {
  const [time12,setTime12] = useState(true);
  const [form] = Form.useForm();
  const [loading, setLoading] = useState(false);
  const [msg, setMsg] = useState<string>('点击抢按钮后，请勿关闭浏览器!!!!!\n');
  const ws = useRef<WebSocket | null>(null);
  const player = new Audio('hyl.mp3')
  player.loop = true;
  
  const onFinish = () => {
    console.log('finishForm')
    setLoading(true);
    // console.log(time12)
    // if(time12){
    //   const interval = setInterval(() => {
    //     console.log('check time')
    //     const now = new Date();
    //     const midnight = new Date(now.getFullYear(), now.getMonth(), now.getDate(), 12, 0, 0, 0);
    //     const timeDiff = midnight.getTime() - now.getTime();
    //     if (timeDiff > 0) {
    //       setMsg( msg => msg + '距离12:00还有' + Math.floor(timeDiff / 1000) + '秒\n');
    //     } else {
    //       setMsg( msg => msg + '12:00已到，开始抢单\n');
    //       clearInterval(interval);
    //     }
    //   }, 1000);
    // }

    
    ws.current = new WebSocket('ws://'+ location.hostname+":"+location.port +apiurl);
    ws.current.onopen = () => {
      ws.current?.send(JSON.stringify(form.getFieldsValue()));
    };
    ws.current.onmessage = e => {
      setMsg( msg => msg + e.data + '\n');
      if (e.data.startsWith('结束工作...')) {
        ws.current?.close();
        alert('抢单结束');
        setLoading(false);
      }
      if (e.data.startsWith('error')){
        message.error(e.data);
        ws.current?.close();
        setLoading(false);
      }
      if (e.data.startsWith('中断...')){
        ws.current?.close();
        setLoading(false);
      }
      if (e.data.startsWith('cookie登录成功')){
        ws.current?.send('continue')
      }
      if (e.data.startsWith('continue')){
        ws.current?.send('continue')
      }
      if (e.data.startsWith('success')){
        if(player.paused)
          player.play();
        console.log('抢单成功') 
      }
    };
    return () => {
      ws.current?.close();
      message.success('已完成');
      // form.resetFields();
      setLoading(false);
    };
  };

  const onFinishFailed = (errorInfo: any) => {
    console.log(time12)
    console.log('Failed:', errorInfo);
    message.error('表单验证失败');
  };

  // useLayoutEffect(() => {
  //   ws.current = new WebSocket('ws://'+apiurl);
  //   ws.current.onmessage = e => {
  //     setMsg(e.data);
  //   };
  //   return () => {
  //     ws.current?.close();
  //   };
  // }, [ws]);


  return (
    <>
      <div className="container" style={{ display: 'flex', height: '100vh' }}>
        <div style={{ flex:1, textAlign: 'center' }}>
          <h1>订单表单</h1>
          <Form
            form={form}
            name="orderForm"
            initialValues={{ remember: true }}
            onFinish={onFinish}
            onFinishFailed={onFinishFailed}
            labelCol={{ span: 8 }}
            wrapperCol={{ span: 16 }}
            style={{ maxWidth: 600 }}
            autoComplete="off"
          >
            <h2>cookies</h2>
            前往<a target='_blank' href='https://tybsouthgym.xidian.edu.cn/'>https://tybsouthgym.xidian.edu.cn/</a>按F12找到三项cookie
            <Form.Item
              label="JWTUserToken"
              name="JWTUserToken"
              rules={[{ required: true, message: '请输入JWTUserToken' }]}
            >
              <Input />
            </Form.Item>

            <Form.Item
              label="UserId"
              name="UserId"
              rules={[{ required: true, message: '请输入UserId' }]}
            >
              <Input />
            </Form.Item>

            <Form.Item
              label="WXOpenId"
              name="WXOpenId"
              rules={[{ required: true, message: '请输入WXOpenId' }]}
            >
              <Input />
            </Form.Item>

            <h2>抢单</h2>
            <Form.Item
              label="订单数量"
              name="demand"
              rules={[
                { required: true, message: '请输入订单数量' },
                { type: 'integer', min: 1, message: '订单数量必须大于0' },
              ]}
            >
              <InputNumber />
            </Form.Item>

            <Form.Item
              label="项目类型"
              name="fieldType"
              rules={[{ required: true, message: '请选择项目类型' }]}
            >
              <Select placeholder="请选择项目类型">
                <Option value={1}>羽毛球</Option>
                <Option value={2}>乒乓球</Option>
                <Option value={3}>篮球</Option>
              </Select>
            </Form.Item>

            <Form.Item
              label="日期"
              name="dateAdd"
              rules={[{ required: true, message: '请选择日期' }]}
            >
              <Select placeholder="请选择日期">
                <Option value={0}>今天</Option>
                <Option value={1}>明天</Option>
                <Option value={2}>后天</Option>
                
              </Select>
            </Form.Item>

            <Form.Item
              label="时间段"
              name="timePeriod"
              rules={[{ required: true, message: '请选择时间段' }]}
            >
              <Select placeholder="请选择时间段">
                <Option value={0}>上午</Option>
                <Option value={1}>下午</Option>
                <Option value={2}>晚上</Option>
              </Select>
            </Form.Item>
            
            <Form.Item
              label = "定时12:00"
              name = "time12"
            >
              <Switch checkedChildren="开启" unCheckedChildren="关闭" checked={time12} onChange={(checked) => {
                setTime12(checked)
              }}/>

            </Form.Item>

            <Form.Item wrapperCol={{ offset: 8, span: 16 }}>
              <Button type="primary" htmlType="submit" loading={loading}>
                {loading ? '正在抢单' : '开抢'}
              </Button>
              <Button type="default" onClick={() => {
                  ws.current?.send('stop')
                  ws.current?.close()
                  setLoading(false)
                }} disabled={!loading}>
                暂停
              </Button>
            </Form.Item>
          </Form>
        </div>
        <div style={{flex:1}}>

          <Button  onClick={()=>{
              if (player.paused)
                player.play()
            }            
          }>测试音乐</Button>
          <Button onClick={()=>{
              player.pause()
              player.currentTime = 0
            }            
          }>别唱了</Button>


          <h1>消息队列</h1>
          <TextArea style={{width:'100%', height:'100%'}} value={msg} disabled ></TextArea>
        </div>
      </div>
    </>
    
  );
}

export default App;