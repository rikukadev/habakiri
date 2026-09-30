<?php
class UserStat extends CActiveRecord
{
    // 直書きの名前には接頭辞を付けない(Yii1 と同じ)
    public function tableName() { return 'tbl_user_stat'; }
}
