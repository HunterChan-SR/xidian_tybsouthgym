package models

type Order struct {
	BeginTime     string `json:"BeginTime"`
	EndTime       string `json:"EndTime"`
	Count         string `json:"Count"`
	FieldNo       string `json:"FieldNo"`
	FieldName     string `json:"FieldName"`
	FieldTypeNo   string `json:"FieldTypeNo"`
	FinalPrice    string `json:"FinalPrice"`
	TimeStatus    string `json:"TimeStatus"`
	FieldState    string `json:"FieldState"`
	IsHalfHour    string `json:"IsHalfHour"`
	ShowWidth     string `json:"ShowWidth"`
	DateBeginTime string `json:"DateBeginTime"`
	DateEndTime   string `json:"DateEndTime"`
	TimePeriod    string `json:"TimePeriod"`
	MembeName     string `json:"MembeName"`
}
type Response struct {
	IsCardPay  *string `json:"IsCardPay"`
	MemberNo   *string `json:"MemberNo"`
	Discount   *string `json:"Discount"`
	ConType    *string `json:"ConType"`
	Type       int     `json:"type"`
	Errorcode  int     `json:"errorcode"`
	Message    string  `json:"message"`
	ResultData string  `json:"resultdata"`
}
