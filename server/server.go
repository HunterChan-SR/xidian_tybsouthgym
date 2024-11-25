package server

import (
	"fmt"
	"net/http"
	xidianTybsouthgymClient "xidian_tybsouthgym/client"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

var upgrade = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
}

// <Form
//             form={form}
//             name="orderForm"
//             initialValues={{ remember: true }}
//             onFinish={onFinish}
//             onFinishFailed={onFinishFailed}
//             labelCol={{ span: 8 }}
//             wrapperCol={{ span: 16 }}
//             style={{ maxWidth: 600 }}
//             autoComplete="off"
//           >
//             <h2>cookies</h2>
//             前往<a target='_blank' href='https://tybsouthgym.xidian.edu.cn/'>https://tybsouthgym.xidian.edu.cn/</a>按F12找到三项cookie
//             <Form.Item
//               label="JWTUserToken"
//               name="JWTUserToken"
//               rules={[{ required: true, message: '请输入JWTUserToken' }]}
//             >
//               <Input />
//             </Form.Item>

//             <Form.Item
//               label="UserId"
//               name="UserId"
//               rules={[{ required: true, message: '请输入UserId' }]}
//             >
//               <Input />
//             </Form.Item>

//             <Form.Item
//               label="WXOpenId"
//               name="WXOpenId"
//               rules={[{ required: true, message: '请输入WXOpenId' }]}
//             >
//               <Input />
//             </Form.Item>

//             <h2>抢单</h2>
//             <Form.Item
//               label="订单数量"
//               name="demand"
//               rules={[
//                 { required: true, message: '请输入订单数量' },
//                 { type: 'integer', min: 1, message: '订单数量必须大于0' },
//               ]}
//             >
//               <InputNumber />
//             </Form.Item>

//             <Form.Item
//               label="项目类型"
//               name="fieldType"
//               rules={[{ required: true, message: '请选择项目类型' }]}
//             >
//               <Select placeholder="请选择项目类型">
//                 <Option value={1}>羽毛球</Option>
//                 <Option value={2}>乒乓球</Option>
//                 <Option value={3}>篮球</Option>
//               </Select>
//             </Form.Item>

//             <Form.Item
//               label="日期"
//               name="dateAdd"
//               rules={[{ required: true, message: '请选择日期' }]}
//             >
//               <Select placeholder="请选择日期">
//                 <Option value={0}>今天</Option>
//                 <Option value={1}>明天</Option>
//                 <Option value={2}>后天</Option>
//               </Select>
//             </Form.Item>

//             <Form.Item
//               label="时间段"
//               name="timePeriod"
//               rules={[{ required: true, message: '请选择时间段' }]}
//             >
//               <Select placeholder="请选择时间段">
//                 <Option value={0}>上午</Option>
//                 <Option value={1}>下午</Option>
//                 <Option value={2}>晚上</Option>
//               </Select>
//             </Form.Item>

//	<Form.Item wrapperCol={{ offset: 8, span: 16 }}>
//	  <Button type="primary" htmlType="submit" loading={loading}>
//	    {loading ? '正在抢单' : '开抢'}
//	  </Button>
//	</Form.Item>
//
// </Form>
type FormData struct {
	JWTUserToken string `json:"JWTUserToken"`
	UserId       string `json:"UserId"`
	WXOpenId     string `json:"WXOpenId"`
	Demand       int    `json:"demand"`
	FieldType    int    `json:"fieldType"`
	DateAdd      int    `json:"dateAdd"`
	TimePeriod   int    `json:"timePeriod"`
}

func Run() {
	engine := gin.Default()

	// 定义 API 路由
	engine.GET("/api", func(ctx *gin.Context) {
		browser_client, _ := upgrade.Upgrade(ctx.Writer, ctx.Request, nil)
		formData := &FormData{}
		err := browser_client.ReadJSON(formData)

		if err != nil {
			browser_client.WriteMessage(websocket.TextMessage, []byte("error:"+err.Error()))
			browser_client.Close()
			return
		} else {
			browser_client.WriteMessage(websocket.TextMessage, []byte("正在解析表单"))
		}
		xidian_client := xidianTybsouthgymClient.New(formData.JWTUserToken, formData.UserId, formData.WXOpenId, formData.Demand)
		isLogin := xidian_client.CheckUserStatus()
		if !isLogin {
			browser_client.WriteMessage(websocket.TextMessage, []byte("error:cookie已失效"))
			browser_client.Close()
			return
		} else {
			browser_client.WriteMessage(websocket.TextMessage, []byte("cookie登录成功"))
		}
		if xidian_client.GetOrderByTime2(formData.FieldType, formData.DateAdd, formData.TimePeriod, browser_client) {
			browser_client.WriteMessage(websocket.TextMessage, []byte("结束工作..."))
			browser_client.Close()
			return
		} else {
			browser_client.WriteMessage(websocket.TextMessage, []byte("中断..."))
			browser_client.Close()
			return
		}
		// 定义静态文件路由
	})
	// 使用中间件处理静态文件
	engine.NoRoute(func(c *gin.Context) {
		http.FileServer(http.Dir("./xidian_tybsouthgym_web/dist")).ServeHTTP(c.Writer, c.Request)
	})
	err := engine.Run(":3080")
	if err != nil {
		fmt.Println(err)
		panic(err)
	}
}
