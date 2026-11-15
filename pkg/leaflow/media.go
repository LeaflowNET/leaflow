package leaflow

func (o *Operation) RequestMediaType() string     { return o.inputs.ContentType }
func (o *Operation) ResponseMediaTypes() []string { return o.spec.ResponseMediaTypes() }
func (o *Operation) FileFields() map[string]bool  { return o.spec.FileFields() }
func (o *Operation) BodyKey() string              { return o.bodyKey }
