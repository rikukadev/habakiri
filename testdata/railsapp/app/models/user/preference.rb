# モデルクラスの中の名前空間: テーブルは「親テーブルの単数形_」+ 名前
class User::Preference < ApplicationRecord
  belongs_to :user
end
